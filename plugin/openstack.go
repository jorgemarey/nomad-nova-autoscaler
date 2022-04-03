package plugin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/apiversions"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
	flavorutils "github.com/gophercloud/utils/openstack/compute/v2/flavors"
	imageutils "github.com/gophercloud/utils/openstack/imageservice/v2/images"
	networkutils "github.com/gophercloud/utils/openstack/networking/v2/networks"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils"
	"github.com/hashicorp/nomad/api"
)

const (
	version = "v0.2.2"
)

const (
	defaultActionTimeout        = 90 // Seconds
	poolTag                     = "na_pool:%s"
	defaultConfigValueSeparator = ","
	configKVSeparator           = "="
	maxConcurrentActions        = "5"
)

// setupOSClients takes the passed config mapping and instantiates the
// required OS service clients.
func (t *TargetPlugin) setupOSClients(config map[string]string) error {
	t.cache = make(map[string]string)

	// use env vars but don't fail if not all are provided
	ao, _ := openstack.AuthOptionsFromEnv()

	if authURL, ok := config[configKeyAuthUrl]; ok {
		ao.IdentityEndpoint = authURL
	}
	if username, ok := config[configKeyUsername]; ok {
		ao.Username = username
	}
	if password, ok := config[configKeyPassword]; ok {
		ao.Password = password
	}
	if domainName, ok := config[configKeyDomainName]; ok {
		ao.DomainName = domainName
	}
	if projectID, ok := config[configKeyProjectID]; ok {
		ao.TenantID = projectID
	}
	if projectName, ok := config[configKeyProjectName]; ok {
		ao.TenantName = projectName
	}
	ao.AllowReauth = true

	provider, err := openstack.NewClient(ao.IdentityEndpoint)
	if err != nil {
		return fmt.Errorf("failed to create OS client: %v", err)
	}
	if err := t.configureTLS(provider, config); err != nil {
		return fmt.Errorf("failed configure TLS options: %v", err)
	}
	if err := openstack.Authenticate(provider, ao); err != nil {
		return fmt.Errorf("failed to authenticate with OS: %v", err)
	}

	if err := t.configureClients(provider, config); err != nil {
		return err
	}

	t.getDefaultAvZones()
	t.getCurrentMicroVersion(t.computeClient)

	t.logger.Info("completed set-up of plugin", "version", version)
	return nil
}

func (t *TargetPlugin) configureTLS(provider *gophercloud.ProviderClient, config map[string]string) error {
	var tlsConfig *tls.Config

	if certFile, ok := config[configKeyCACertFile]; ok {
		caCert, err := ioutil.ReadFile(certFile)
		if err != nil {
			return err
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)
		tlsConfig = &tls.Config{RootCAs: caCertPool}
	}

	if _, ok := config[configKeyInsecure]; ok {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
	}
	provider.HTTPClient.Transport = transport

	return nil
}

func (t *TargetPlugin) configureClients(provider *gophercloud.ProviderClient, config map[string]string) error {
	regionName := "RegionOne"
	if region, ok := config[configKeyRegionName]; ok {
		regionName = region
	}

	computeClient, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{Region: regionName})
	if err != nil {
		return fmt.Errorf("failed to create OS compute client: %v", err)
	}
	t.computeClient = computeClient

	imageClient, err := openstack.NewImageServiceV2(provider, gophercloud.EndpointOpts{Region: regionName})
	if err != nil {
		return fmt.Errorf("failed to create OS image client: %v", err)
	}
	t.imageClient = imageClient

	networkClient, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{Region: regionName})
	if err != nil {
		return fmt.Errorf("failed to create OS network client: %v", err)
	}
	t.networkClient = networkClient

	t.computeClient.Microversion = "2.52"
	return nil
}

func (t *TargetPlugin) getDefaultAvZones() {
	allPages, err := availabilityzones.List(t.computeClient).AllPages()
	if err != nil {
		t.logger.Warn(fmt.Sprintf("failed to list nova availability zones: %s", err))
		return
	}

	availabilityZoneInfo, err := availabilityzones.ExtractAvailabilityZones(allPages)
	if err != nil {
		t.logger.Warn(fmt.Sprintf("failed to extract availability zones data: %s", err))
		return
	}

	if len(availabilityZoneInfo) == 0 {
		t.logger.Warn("No information about AV zones was discovered")
		return
	}

	zones := make([]string, 0)
	for _, zoneInfo := range availabilityZoneInfo {
		if zoneInfo.ZoneName != "nova" { // do not use default nova AZ
			zones = append(zones, zoneInfo.ZoneName)
		}
	}
	t.logger.Info(fmt.Sprintf("discovered the following AZs: %s, saving as default", zones))
	t.avZones = zones
}

func (t *TargetPlugin) getCurrentMicroVersion(client *gophercloud.ServiceClient) {
	allPages, err := apiversions.List(client).AllPages()
	if err != nil {
		t.logger.Warn(fmt.Sprintf("failed to list compute api versions: %s", err))
		return
	}

	versions, err := apiversions.ExtractAPIVersions(allPages)
	if err != nil {
		t.logger.Warn(fmt.Sprintf("failed to extract compute api version data: %s", err))
		return
	}

	var current string
	for _, version := range versions {
		if version.Status == "CURRENT" {
			current = version.Version
			break
		}
	}

	if current != "" {
		client.Microversion = current
		t.logger.Info(fmt.Sprintf("discovered current microversion %s for %s, making it the used one", current, client.Type))
	}
}

// scaleOut updates the Auto Scaling Group desired count to match what the
// Autoscaler has deemed required.
func (t *TargetPlugin) scaleOut(ctx context.Context, count int64, azDist map[string]int, config map[string]string) error {
	log := t.logger.With("action", "scale_out", "pool_name", config[configKeyPoolName], "desired_count", count)

	log.Debug("getting creation data from configuration")
	createData, err := t.getCreateData(ctx, config)
	if err != nil {
		return err
	}

	if err = t.createServers(ctx, int(count), azDist, createData); err != nil {
		return err
	}

	log.Info("successfully performed and verified scaling out")
	return nil
}

// scaleIn updates the Auto Scaling Group desired count to match what the
// Autoscaler has deemed required.
func (t *TargetPlugin) scaleIn(ctx context.Context, count int64, config map[string]string) error {
	ids, err := t.clusterUtils.RunPreScaleInTasks(ctx, config, int(count))
	if err != nil {
		return fmt.Errorf("failed to perform pre-scale Nomad scale in tasks: %v", err)
	}

	// Grab the instanceIDs
	var instanceIDs []string

	for _, node := range ids {
		instanceIDs = append(instanceIDs, node.RemoteResourceID)
	}

	pool := config[configKeyPoolName]

	// Create a logger for this action to pre-populate useful information we
	// would like on all log lines.
	log := t.logger.With("action", "scale_in", "pool_name", pool, "instances", ids)

	// Delete the instances from the Managed Instance Groups. The targetSize of the MIG is will be reduced by the
	// number of instances that are deleted.
	log.Debug("deleting OS Nova instances")

	stopFirst := config[configKeyStopFirst] != ""
	forceDelete := config[configKeyForceDelete] != ""
	if err := t.deleteServers(ctx, pool, stopFirst, forceDelete, instanceIDs); err != nil {
		return fmt.Errorf("failed to delete instances: %v", err)
	}

	log.Info("successfully deleted OS Nova instances")

	// Run any post scale in tasks that are desired.
	if err := t.clusterUtils.RunPostScaleInTasks(ctx, config, ids); err != nil {
		return fmt.Errorf("failed to perform post-scale Nomad scale in tasks: %v", err)
	}

	log.Info("successfully performed and verified scaling in")
	return nil
}

func (t *TargetPlugin) createServers(ctx context.Context, count int, azDist map[string]int, common *commonCreateData) error {
	customCDList := make([]*customCreateData, count)

	for i := range customCDList {
		name := common.name
		randomUUID := generateUUID()
		if p := common.namePrefix; p != "" {
			name = fmt.Sprintf("%s%s", p, randomUUID[0:13])
		}
		customCDList[i] = &customCreateData{name: name, randomUUID: randomUUID}
	}

	if common.evenlydistributeAZ {
		azList := t.avZones
		if len(common.availabilityZones) > 0 {
			azList = common.availabilityZones
		}
		distributeAZ(azList, azDist, customCDList)
	}

	for _, custom := range customCDList {
		if err := t.createServer(ctx, common, custom); err != nil {
			return err
		}
	}
	return nil
}

func (t *TargetPlugin) createServer(ctx context.Context, common *commonCreateData, custom *customCreateData) error {
	opts, err := dataToCreateOpts(common, custom)
	if err != nil {
		return fmt.Errorf("failed to initialize server options: %s", err)
	}

	t.logger.Debug("creating instances")
	server, err := servers.Create(t.computeClient, opts).Extract()
	if err != nil {
		return fmt.Errorf("failed to create server: %s", err)
	}

	t.logger.Debug("waiting for active status", "server", server.ID)
	if err := servers.WaitForStatus(t.computeClient, server.ID, "ACTIVE", t.actionTimeout); err != nil {
		return fmt.Errorf("error waiting for server id %s to get to ACTIVE status: %v", server.ID, err)
	}

	t.logger.Debug("instances boot up completed")

	return nil
}

func (t *TargetPlugin) deleteServers(ctx context.Context, pool string, stopFirst, forceDelete bool, instanceIDs []string) error {
	if t.idMapper {
		for _, id := range instanceIDs {
			if err := t.deleteServer(ctx, stopFirst, forceDelete, id); err != nil {
				return err
			}
		}
		return nil
	}

	var instanceNameMap = map[string]bool{}
	for _, id := range instanceIDs {
		instanceNameMap[id] = false
	}

	pager := servers.List(t.computeClient, servers.ListOpts{Tags: fmt.Sprintf(poolTag, pool)})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}
		for _, server := range serverList {
			if _, ok := instanceNameMap[server.Name]; ok {
				if err := t.deleteServer(ctx, stopFirst, forceDelete, server.ID); err != nil {
					return false, err
				}
				instanceNameMap[server.Name] = true
			}
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	for name, deleted := range instanceNameMap {
		if !deleted {
			return fmt.Errorf("instance with name %s not found", name)
		}
	}
	return nil
}

func (t *TargetPlugin) deleteServer(ctx context.Context, stopFirst, forceDelete bool, instanceID string) error {
	log := t.logger.With("action", "delete", "instance_id", instanceID)

	if stopFirst {
		log.Debug("stopping instance")
		if err := startstop.Stop(t.computeClient, instanceID).ExtractErr(); err != nil {
			return fmt.Errorf("failed to stop server id %s: %v", instanceID, err)
		}
		log.Debug("waiting for shutoff status")
		if err := servers.WaitForStatus(t.computeClient, instanceID, "SHUTOFF", t.actionTimeout); err != nil {
			return fmt.Errorf("error waiting for server id %s to get to SHUTOFF status: %v", instanceID, err)
		}
		log.Debug("instance shutoff completed")
	}

	log.Debug("deleting instance")
	if forceDelete {
		if err := servers.ForceDelete(t.computeClient, instanceID).ExtractErr(); err != nil {
			return fmt.Errorf("failed to delete server id %s: %v", instanceID, err)
		}
	} else {
		if err := servers.Delete(t.computeClient, instanceID).ExtractErr(); err != nil {
			return fmt.Errorf("failed to delete server id %s: %v", instanceID, err)
		}
	}
	log.Debug("waiting for instance deletion")
	if err := gophercloud.WaitFor(t.actionTimeout, func() (bool, error) {
		current, err := servers.Get(t.computeClient, instanceID).Extract()
		if err != nil {
			if _, ok := err.(gophercloud.ErrDefault404); ok {
				return true, nil
			}
			return false, err
		}

		if current.Status == "DELETED" || current.Status == "SOFT_DELETED" {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("error waiting for server id %s to get to DELETED status: %v", instanceID, err)
	}
	log.Debug("instance deletion completed")
	return nil
}

type customServer struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	AZ       string            `json:"OS-EXT-AZ:availability_zone"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
	Tags     *[]string         `json:"tags"`
}

func (t *TargetPlugin) countServers(ctx context.Context, pool string) (int64, int64, map[string]int, error) {
	var total int64
	var ready int64
	azDist := make(map[string]int)

	pager := servers.List(t.computeClient, servers.ListOpts{Tags: fmt.Sprintf(poolTag, pool)})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		var serverList []customServer
		if err := servers.ExtractServersInto(page, &serverList); err != nil {
			return false, err
		}
		total += int64(len(serverList))
		for _, v := range serverList {
			if v.Status == "ACTIVE" {
				ready += 1
			}
			azDist[v.AZ] = azDist[v.AZ] + 1
		}
		return true, nil
	})
	return total, ready, azDist, err
}

type customCreateData struct {
	name             string
	availabilityzone string
	randomUUID       string
}

type commonCreateData struct {
	name               string
	namePrefix         string
	pool               string
	imageID            string
	flavorID           string
	securityGroups     []string
	networkUUID        string
	availabilityZones  []string
	evenlydistributeAZ bool
	userDataTemplate   string
	metadata           map[string]string
	tags               []string
}

func (t *TargetPlugin) getCreateData(ctx context.Context, config map[string]string) (*commonCreateData, error) {
	data := &commonCreateData{
		name:               config[configKeyName],
		namePrefix:         config[configKeyNamePrefix],
		pool:               config[configKeyPoolName],
		userDataTemplate:   config[configKeyUserDataT],
		evenlydistributeAZ: config[configKeyESAZ] != "",
	}
	configValueSeparator := defaultConfigValueSeparator
	if sep, ok := config[configKeyValueSeparator]; ok && sep != "" {
		configValueSeparator = sep
	}

	if data.name != "" && data.namePrefix != "" {
		return nil, fmt.Errorf("only one of %s or %s can have value", configKeyName, configKeyNamePrefix)
	}

	imageID, err := t.getImageID(ctx, config)
	if err != nil {
		return nil, err
	}
	data.imageID = imageID

	flavorInfo, err := t.getFlavorInfo(ctx, config)
	if err != nil {
		return nil, err
	}
	data.flavorID = flavorInfo.flavorID

	networkID, err := t.getNetworkID(ctx, config)
	if err != nil {
		return nil, err
	}
	data.networkUUID = networkID

	if sgNames, ok := config[configKeySGNames]; ok && strings.TrimSpace(sgNames) != "" {
		sgs := strings.Split(strings.TrimSpace(sgNames), configValueSeparator)
		data.securityGroups = make([]string, len(sgs))
		for i, name := range sgs {
			data.securityGroups[i] = strings.TrimSpace(name)
		}
	}

	if metadata, ok := config[configKeyMetadata]; ok && strings.TrimSpace(metadata) != "" {
		metadataList := strings.Split(strings.TrimSpace(metadata), configValueSeparator)
		data.metadata = make(map[string]string)
		for _, v := range metadataList {
			kv := strings.Split(v, configKVSeparator)
			if len(kv) != 2 {
				t.logger.Warn("metadata value is not correctly provided", "element", kv)
				continue
			}
			data.metadata[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}

	if tags, ok := config[configKeyTags]; ok && strings.TrimSpace(tags) != "" {
		tagList := strings.Split(strings.TrimSpace(tags), configValueSeparator)
		data.tags = make([]string, len(tagList))
		for i, tag := range tagList {
			data.tags[i] = strings.TrimSpace(tag)
		}
	}

	if zones, ok := config[configKeyAvZones]; ok && strings.TrimSpace(zones) != "" {
		zoneList := strings.Split(strings.TrimSpace(zones), configValueSeparator)
		data.availabilityZones = make([]string, len(zoneList))
		for i, name := range zoneList {
			data.availabilityZones[i] = strings.TrimSpace(name)
		}
	}

	if data.userDataTemplate != "" {
		if _, err := os.Stat(data.userDataTemplate); err != nil {
			return nil, fmt.Errorf("error with provided template file: %s", err)
		}
	}

	return data, nil
}

type flavorInfo struct {
	flavorID string
}

func (t *TargetPlugin) getFlavorInfo(ctx context.Context, config map[string]string) (*flavorInfo, error) {
	if id, ok := config[configKeyFlavorID]; ok {
		return &flavorInfo{flavorID: id}, nil
	}

	flavorName, ok := config[configKeyFlavorName]
	if !ok {
		return nil, fmt.Errorf("required config param %s or %s", configKeyFlavorID, configKeyFlavorName)
	}

	key := cachekey(flavorCacheKey, flavorName)
	if id, ok := t.cache[key]; ok {
		return &flavorInfo{flavorID: id}, nil
	}

	t.logger.Debug("searching for flavor", "name", flavorName)
	flavorID, err := flavorutils.IDFromName(t.computeClient, flavorName)
	if err != nil {
		return nil, fmt.Errorf("failed to find flavor with name %s", flavorName)
	}
	t.logger.Debug("found flavor ID", "name", flavorName, "id", flavorID)

	t.cache[key] = flavorID
	return &flavorInfo{flavorID: flavorID}, nil
}

func (t *TargetPlugin) getImageID(ctx context.Context, config map[string]string) (string, error) {
	if id, ok := config[configKeyImageID]; ok {
		return id, nil
	}

	imageName, ok := config[configKeyImageName]
	if !ok {
		return "", fmt.Errorf("required config param %s or %s", configKeyImageID, configKeyImageName)
	}

	key := cachekey(imageCacheKey, imageName)
	if id, ok := t.cache[key]; ok {
		return id, nil
	}

	t.logger.Debug("searching for image", "name", imageName)
	imageID, err := imageutils.IDFromName(t.imageClient, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to find image with name %s: %s", imageName, err)
	}
	t.logger.Debug("found image ID", "name", imageName, "id", imageID)

	t.cache[key] = imageID
	return imageID, nil
}

func (t *TargetPlugin) getNetworkID(ctx context.Context, config map[string]string) (string, error) {
	if id, ok := config[configKeyNetworkID]; ok {
		return id, nil
	}

	networkName, ok := config[configKeyNetworkName]
	if !ok {
		return "", fmt.Errorf("required config param %s or %s", configKeyNetworkID, configKeyNetworkName)
	}

	key := cachekey(networkCacheKey, networkName)
	if id, ok := t.cache[key]; ok {
		return id, nil
	}

	t.logger.Debug("searching for network", "name", networkName)
	networkID, err := networkutils.IDFromName(t.networkClient, networkName)
	if err != nil {
		return "", fmt.Errorf("failed to find network with name %s: %s", networkName, err)
	}
	t.logger.Debug("found network ID", "name", networkName, "id", networkID)

	t.cache[key] = networkID
	return networkID, nil
}

// osNovaNodeIDMapBuilder is used to identify the Opensack Nova ID of a Nomad node using
// the relevant attribute value.
func osNovaNodeIDMapBuilder(property string) scaleutils.ClusterNodeIDLookupFunc {
	var isMeta bool
	if property == "" {
		property = "unique.platform.aws.hostname"
	}
	if strings.HasPrefix(property, "meta.") {
		isMeta = true
		property = strings.TrimPrefix(property, "meta.")
	}

	return func(n *api.Node) (string, error) {
		mapToUse := n.Attributes
		if isMeta {
			mapToUse = n.Meta
		}

		val, ok := mapToUse[property]
		if !ok || val == "" {
			return "", fmt.Errorf("attribute %q not found", property)
		}
		return val, nil
	}
}
