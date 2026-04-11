# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and rewrites a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change automatically (it watches the file by default).

Desired state is always rebuilt from the full Service list — no incremental patching. Self-healing on pod restart and node reboot.

## Annotation

Add to any `type: LoadBalancer` Service:

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

The controller ignores Services without this annotation and requeues with backoff if a LoadBalancer IP hasn't been assigned yet.

## Hosts file block

The controller owns only a marked block and never touches the rest of the file:

```
# your static entries above are untouched

### BEGIN k8s-avahi-controller ###
192.168.1.100 myapp.local
192.168.1.101 otherapp.local
### END k8s-avahi-controller ###
```

Writes are atomic: content is written to a temp file in the same directory, then `rename()`d over the original.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on shutdown |
| `--reload` | `false` | Signal avahi-daemon via systemd D-Bus after each write (modern avahi watches hosts files for changes)|
| `--avahi-service` | `avahi-daemon.service` | systemd unit name (override for distros using `avahi.service`) |
| `--resync-period` | `10m` | Informer resync interval |
| `--verbose` | `false` | Log reconcile details (qualifying services, scan summary) |
| `--debug` | `false` | Log everything including client-go internals (very noisy) |

## Avahi reload

By default (`--reload=false`) avahi-daemon notices file changes on its own — no signalling needed. Enable `--reload` only if your setup requires an explicit reload. When enabled, the controller calls `systemctl reload <avahi-service>` via the system D-Bus, which sends SIGHUP to avahi-daemon.

Requires the system D-Bus socket mounted into the pod at `/run/dbus/system_bus_socket`.

## Edge cases

| Scenario | Handling |
|---|---|
| MetalLB reassigns IP | Update event → entry rewritten |
| Service deleted | Entry removed immediately |
| Node reboots, file wiped | Pod reschedules to same node; startup reconcile rewrites |
| Two Services claim same hostname | First writer wins; second is skipped with a log error and Kubernetes Event |
| Annotation added to existing Service | Treated as new entry |
| Annotation removed | Entry removed, same as delete |
| Pod restarts, file already correct | Hash match → no write, no reload |

## Requirements

- `avahi-daemon` installed and running on the designated node
- Node labeled with `avahi-controller: "true"` for pod scheduling
- RBAC: `get/list/watch` on Services (cluster-wide), `create/patch` on Events

## Deployment

<!-- TODO: Helm chart / raw manifests -->

## Docker image

```
mm404/avahi-controller:<version>
```

Available tags: `latest`, `x.y.z`, and `dev-*` builds from feature branches.
