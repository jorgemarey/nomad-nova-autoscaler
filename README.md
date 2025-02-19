# Nomad Openstack Nova Autoscaler

This repo contains the `os-nova` nomad autoscaler target plugin. It allow fot the scaling of Nomad clients
by creating and deleting Openstack Nova Compute instances.

## Requirements

* nomad autoscaler 0.3.3+

## Documentation

### Agent Configuration

You need to configure the target plugin correctly for this to work. That can be done either by providing configuration in the nomad
autoscaler configuration file or by the defined environment variables commonly used in [Openstack](https://pkg.go.dev/github.com/gophercloud/gophercloud/openstack#AuthOptionsFromEnv).

```hcl
target "os-nova" {
  driver = "os-nova"
  config = {
    auth_url    = "https://myopenstack.com"
    username    = "username"
    password    = "supersecurepassword"
    domain_name = "mydomain"
    project_id  = "424frwdfsd3456tsdfs2"
  }
}
```

* `auth_url` `(string: "")` - The authentication URL of opentack
* `project_name` `(string: "")` - The name of the project
* `project_id` `(string: "")` - The id of the project
* `username` `(string: "")` - The username to use when authenticating
* `password` `(string: "")` - The password to use when authenticating
* `region_name` `(string: "")` - The services region name to use
* `domain_name` `(string: "")` - The domain of the user
* `cacert_file` `(string: "")` - Location of the certificate to use for OS APIs verification
* `insecure_skip_verify` `(string: "")` - Skip TLS certificate verification

* `name_attribute` `(string: "unique.platform.aws.hostname")` - The nomad attribute that reflects the instance name. This needs to be used for searching the instance ID in the proccess of downscaling
* `id_attribute` `(string: "")` - The nomad attribute to use that maps the nomad client to an OS Compute instance. If not specified then a previous search is needed to get the instance id using the instance name using `name_attribute`. If this is specified it takes priority over `name_attribute`
* `action_timeout` `(string: "")` - The timeout to use when performing create and delete actions over servers. This should be specified as a duration. The default vaule is 90s
* `ignored_states` `(string: "")` - A comma-separated list of server states to be ignored. The complete list can be seen [here](https://docs.openstack.org/api-guide/compute/server_concepts.html)

### Policy Configuration

```hcl
scaling "worker_pool_policy" {
  # ...

  policy {
    # ...

    check "cpu_allocated_percentage" {
      # ...
    }

    target "os-nova" {
      dry-run = false

      evenly_split_azs   = true
      stop_first         = true
      image_name         = "myimage-v1"
      flavor_name        = "t1.large"
      pool_name          = "test-pool"
      name_prefix        = "managed-pool-"
      network_id         = "c114a407-b11e-4b57-9c3e-5c461b91435a"
      user_data_template = "user-data.gotxt"
      security_groups    = "consul,nomad,default"

      node_class                    = "wrkr-test"
      node_drain_deadline           = "1h"
      node_drain_ignore_system_jobs = false
      node_purge                    = true
      node_selector_strategy        = "least_busy"
    }
  }
}
```

* `name` `(string: "")` - Instance name to set when creating
* `name_prefix` `(string: "")` - Use a prefix with a random generated trailing instead of a fix name. One of `name` or `name_prefix` must be set
* `pool_name` `(string: <required>)` - The pool name of the instances. This will be set as a intance tag to later find all instances magaged by this plugin.
* `image_id` `(string: "")` - The image ID to use when creating servers
* `image_name` `(string: "")` - The image name to use. One of `image_id` or `image_name` must be set
* `flavor_id` `(string: "")` - The flavor ID to use when creating servers
* `flavor_name` `(string: "")` - The flavor name to use. One of `flavor_id` or `flavor_name` must be set
* `availavility_zones` `(string: "")` - The list of AZ that intances can be launched in. By default the plugin will search for all the available zones.
If no zones are provided, and none are discovered, a random one will be asigned by Nova
* `evenly_split_azs` `(string: "")` - Set this to any value other than blank to try to balance the instances over the provided AZs when creating/destroying
* `server_group_id` `(string: "")` - The server group ID to use for the scheduler to place the server
* `network_id` `(string: "")` - The network ID where to lauch the servers
* `network_name` `(string: "")` - The network name to use. One of `network_id` or `network_name` must be set
* `floatingip_pool_name` `(string: "")` - The floating ip network name to use. If this is specified a new floating ip will be allocated and attached to every created instance
* `security_groups` `(string: "")` - A comma-separated list of SG names to provide on creation
* `user_data_template` `(string: "")` - The path to a file containing the user data for the instance creation. This will be treated as a golang
template, so {{ }} characters will be executed. `.Name`, `.AZ`, `.RandomUUID` and `.PoolName` can be used
* `metadata` `(string: "")` - A comma-separated, equal-separated key value items to add to the servers. e.g. "k1=v,k2=b"
* `tags` `(string: "")` - A comma-separated list of tags to apply on the servers
* `value_separator` `(string: ",")` - Separator to use when splitting configuraiton options that are used as lists. Changing this value will afect the
separator used in `availavility_zones`, `security_groups`, `metadata` and `tags`.

* `stop_first` `(string: "")` - Set this to any value other than blank to signal that servers must be stopped before deleted.
* `force_delete` `(string: "")` - Set this to any value other than blank to use the force when deleting servers :)
