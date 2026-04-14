# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change automatically (it watches the file by default).

This can be particularly useful in combination with MetalLB, which assigns LAN IPs to your Kubernetes LoadBalancers.

Please note, this is not a production tool. mDNS is not a production-level solution. This is for a home lab and simple Kubernetes clusters.

## Annotation

Add to any `type: LoadBalancer` Service:

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

The controller ignores Services without this annotation, and requeues with backoff if a LoadBalancer IP hasn't been assigned yet.

## Hosts file block

The controller owns only a marked block and never touches the rest of the file:

```
# your static entries above are untouched

### BEGIN k8s-avahi-controller ###
# Managed by avahi-controller. Do not edit between these markers.
192.168.1.100 myapp.local
192.168.1.101 otherapp.local
### END k8s-avahi-controller ###
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on controller shutdown |
| `--reload` | `false` | Signal avahi-daemon via systemd D-Bus after each write (modern avahi watches hosts files for changes)|
| `--avahi-service` | `avahi-daemon.service` | systemd unit name (override for distros using `avahi.service`) |
| `--resync-period` | `10m` | Informer resync interval |
| `--verbose` | `false` | Verbose logging: per-service reconcile details and scan summary |
| `--debug` | `false` | Debug logging: same as verbose plus client-go internals (very noisy) |

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
| **Manual edit** to hosts file using a rename-based editor (nano, vim, etc.) | Editor creates a new file inode; the pod's hostPath bind mount (which is a single-file mount) still points to the old inode. Subsequent controller writes silently go to the old inode and are invisible to the host. **Workaround**: restart the pod to re-bind to the current inode. **This is a known tradeoff** of mounting a single file via hostPath; mounting the parent directory instead would fix it but widens the volume scope. |

## Requirements

- `avahi-daemon` installed and running on the designated node
- Node labeled with `avahi-controller: "true"` for pod scheduling
- RBAC: `get/list/watch` on Services (cluster-wide), `create/patch` on Events

## Deployment

### Label the node

This is needed even on a single-node cluster (such as k3s and similar). Without the label, the pod's `nodeSelector` will not match any node and the pod will remain Pending. The controller is intentionally pinned to one node to ensure only one instance manages the hosts file on the node where `avahi-daemon` is running.

Label the node where `avahi-daemon` is running:

```sh
kubectl label node <node-name> avahi-controller=true
```

### Helm

Install the chart into `kube-system`:

```sh
helm repo add avahi-controller https://mm503.github.io/avahi-controller
helm install avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

Or from a local clone:

```sh
helm install avahi-controller ./charts/avahi-controller \
  --namespace kube-system
```

#### Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `mm404/avahi-controller` | Container image repository |
| `image.tag` | *(chart appVersion)* | Image tag override |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `args` | `[--cleanup-on-exit=false, --verbose]` | Controller flags |
| `nodeSelector` | `{avahi-controller: "true"}` | Node selector for scheduling |
| `tolerations` | `[{operator: Exists}]` | Tolerates any taint on the designated node |
| `resources.requests` | `cpu: 10m, memory: 32Mi` | Resource requests |
| `resources.limits` | `cpu: 50m, memory: 64Mi` | Resource limits |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |

Example — enable `--reload` and point at a custom avahi service unit:

```sh
helm install avahi-controller ./charts/avahi-controller \
  --namespace kube-system \
  --set args="{--reload=true,--avahi-service=avahi.service,--verbose}"
```

#### Upgrade

```sh
helm repo update avahi-controller
helm upgrade avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

### Raw manifests

```sh
kubectl apply -f deploy/
```

## Image

```
mm404/avahi-controller:<version>
```

Available tags: `latest`, `x.y.z`, and `dev-*` builds from feature branches.
