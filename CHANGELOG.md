## 0.3.0 (Sep 6, 2022)

NOTES:
* This version changes the default behaviour when counting the servers in the pool.
Previously servers in state `ERROR` were ignored. If you want to have the same
behaviour as previously that can be configured using the new `ignored_states`
configuration option

FEATURES:
* Allow setting a `ignored_states` to ignore server in these states when counting

## 0.2.5 (Apr 11, 2022)

BUG FIXES:
* Fix scale-in when using node name as the mapper function

## 0.2.4 (Apr 5, 2022)

BUG FIXES:
* Fix deletion of all servers when scaling in.

## 0.2.3 (Apr 3, 2022)

NOTES:
* This version contains a bug related to https://github.com/hashicorp/nomad-autoscaler/issues/572
that will destroy all nodes in the pool when scaling in. You should skip this and upgrade to 0.2.4

FEATURES:
* Allow configuring a timeout for creation and deletion of servers
* Allow setting a `value_separator` to use when splitting value strings
* Update autoscaler library to v0.3.6

BUG FIXES:
* Fix use of `id_attribute` failing after the first use.
* Do not count servers in ERROR state towards the total

## 0.2.2 (Oct 14, 2021)

FEATURES:
* Allow using `meta.` attributes to map Nova server ID

BUG FIXES:
* Fix server deletion when filter is by name
* Return an error is a server was not found when deleting

## 0.2.1 (Oct 13, 2021)

FEATURES:
* Include `cacert_file`  and `insecure_skip_verify` options

BUG FIXES:
* Fix AZ list (correctly remove nova default AZ)

## 0.2.0 (Oct 13, 2021)

FEATURES:
* Allow using networks by name

IMPROVEMENTS:
* Add cache to avoid API calls when name values are the same. Used for image and flavor

BUG FIXES:
* Use correct import to discover nova AZs

## 0.1.0 (Oct 6, 2021)

Initial release