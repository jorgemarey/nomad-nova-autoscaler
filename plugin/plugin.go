package plugin

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad-autoscaler/plugins"
	"github.com/hashicorp/nomad-autoscaler/plugins/base"
	"github.com/hashicorp/nomad-autoscaler/sdk"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/nomad"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils"
)

const (
	// pluginName is the unique name of the this plugin amongst Target plugins.
	pluginName = "os-nova"

	configKeyAuthUrl     = "auth_url"
	configKeyProjectName = "project_name"
	configKeyProjectID   = "project_id"
	configKeyUsername    = "username"
	configKeyPassword    = "password"
	configKeyRegionName  = "region_name"
	configKeyDomainName  = "domain_name"

	configKeyNodeIDAttr = "id_attribute"

	configKeyName        = "name"
	configKeyNamePrefix  = "name_prefix"
	configKeyPoolName    = "pool_name"
	configKeyImageID     = "image_id"
	configKeyImageName   = "image_name"
	configKeyFlavorID    = "flavor_id"
	configKeyFlavorName  = "flavor_name"
	configKeyAvZones     = "availavility_zones" // default is to leave AZ blank for nova to fill
	configKeyESAZ        = "evenly_split_azs"
	configKeyNetworkID   = "network_id"
	configKeyNetworkName = "network_name"
	configKeySGNames     = "security_groups" // comma separated values
	configKeyUserDataT   = "user_data_template"
	// TODO: configKeyServerGroup = "server_group"
	configKeyMetadata = "metadata" // comma separated k=v values
	configKeyTags     = "tags"     // comma separated values

	configKeyStopFirst   = "stop_first"
	configKeyForceDelete = "force_delete"
)

var (
	PluginConfig = &plugins.InternalPluginConfig{
		Factory: func(l hclog.Logger) interface{} { return NewOSNovaPlugin(l) },
	}

	pluginInfo = &base.PluginInfo{
		Name:       pluginName,
		PluginType: sdk.PluginTypeTarget,
	}
)

// TargetPlugin is the AWS ASG implementation of the target.Target interface.
type TargetPlugin struct {
	config        map[string]string
	logger        hclog.Logger
	computeClient *gophercloud.ServiceClient
	imageClient   *gophercloud.ServiceClient
	networkClient *gophercloud.ServiceClient

	idMapper bool
	avZones  []string
	cache    map[string]string

	// clusterUtils provides general cluster scaling utilities for querying the
	// state of nodes pools and performing scaling tasks.
	clusterUtils *scaleutils.ClusterScaleUtils
}

// NewOSNovaPlugin returns the OS Nova implementation of the target.Target
// interface.
func NewOSNovaPlugin(log hclog.Logger) *TargetPlugin {
	return &TargetPlugin{
		logger: log,
	}
}

// SetConfig satisfies the SetConfig function on the base.Base interface.
func (t *TargetPlugin) SetConfig(config map[string]string) error {
	t.config = config

	if err := t.setupOSClients(config); err != nil {
		return err
	}

	clusterUtils, err := scaleutils.NewClusterScaleUtils(nomad.ConfigFromNamespacedMap(config), t.logger)
	if err != nil {
		return err
	}

	// Store and set the remote ID callback function.
	t.clusterUtils = clusterUtils
	t.clusterUtils.ClusterNodeIDLookupFunc = osNovaNodeIDMapBuilder(config[configKeyNodeIDAttr])
	t.idMapper = config[configKeyNodeIDAttr] != ""

	return nil
}

// PluginInfo satisfies the PluginInfo function on the base.Base interface.
func (t *TargetPlugin) PluginInfo() (*base.PluginInfo, error) {
	return pluginInfo, nil
}

// Scale satisfies the Scale function on the target.Target interface.
func (t *TargetPlugin) Scale(action sdk.ScalingAction, config map[string]string) error {
	// OS can't support dry-run like Nomad, so just exit.
	if action.Count == sdk.StrategyActionMetaValueDryRunCount {
		return nil
	}

	// We cannot scale a pool without knowing the pool name.
	pool, ok := config[configKeyPoolName]
	if !ok {
		return fmt.Errorf("required config param %s not found", configKeyPoolName)
	}

	ctx := context.Background()
	total, _, azDist, err := t.countServers(ctx, pool)
	if err != nil {
		return fmt.Errorf("failed to count Nova servers: %v", err)
	}

	diff, direction := t.calculateDirection(total, action.Count)
	switch direction {
	case "in":
		err = t.scaleIn(ctx, diff, config)
	case "out":
		err = t.scaleOut(ctx, diff, azDist, config)
	default:
		t.logger.Info("scaling not required", "pool_name", pool, "current_count", total, "strategy_count", action.Count)
		return nil
	}

	// If we received an error while scaling, format this with an outer message
	// so its nice for the operators and then return any error to the caller.
	if err != nil {
		err = fmt.Errorf("failed to perform scaling action: %v", err)
	}
	return err
}

// Status satisfies the Status function on the target.Target interface.
func (t *TargetPlugin) Status(config map[string]string) (*sdk.TargetStatus, error) {
	// Perform our check of the Nomad node pool. If the pool is not ready, we
	// can exit here and avoid calling the AWS API as it won't affect the
	// outcome.
	ready, err := t.clusterUtils.IsPoolReady(config)
	if err != nil {
		return nil, fmt.Errorf("failed to run Nomad node readiness check: %v", err)
	}
	if !ready {
		return &sdk.TargetStatus{Ready: ready}, nil
	}

	// We cannot get the status of a pool without knowing the pool name.
	pool, ok := config[configKeyPoolName]
	if !ok {
		return nil, fmt.Errorf("required config param %s not found", configKeyPoolName)
	}

	ctx := context.Background()
	total, active, _, err := t.countServers(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("failed to count Nova servers: %v", err)
	}

	resp := &sdk.TargetStatus{
		Ready: total == active,
		Count: total,
		Meta:  make(map[string]string),
	}
	return resp, nil
}

func (t *TargetPlugin) calculateDirection(target, desired int64) (int64, string) {
	if desired < target {
		return target - desired, "in"
	}
	if desired > target {
		return desired - target, "out"
	}
	return 0, ""
}
