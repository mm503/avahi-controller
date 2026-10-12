# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change by itself; see [How avahi picks up changes](#how-avahi-picks-up-changes).

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

## How avahi picks up changes

The controller never signals avahi-daemon. avahi-daemon watches its config
directory (`/etc/avahi`) with inotify, and whenever a file there that was
opened for writing is closed, it re-reads its configuration, including
`/etc/avahi/hosts`. New names are published and names that disappeared from
the file are withdrawn. This is the same reload that `systemctl reload
avahi-daemon` (SIGHUP) performs, so no D-Bus access or restart is needed.

Because opening the file for writing is enough to make avahi reload, the
controller compares a hash of the desired block with the file first and only
writes when something changed.

avahi reads the hosts file top to bottom and **stops at the first invalid
line**: every line must be exactly one IP address and one hostname (no
aliases). A bad static line above the managed block prevents the controller's
entries, and any removals, from being applied. Look for `Junk on the end of
the line` or `failed to parse address` in `journalctl -u avahi-daemon`.

## How the pod shares the file

The pod mounts only `/etc/avahi/hosts` from the node (a single-file
`hostPath`), not the whole `/etc/avahi` directory. A single-file mount binds
the container to the file's inode, and the path inside the pod is a mount
point that cannot be replaced, so the controller rewrites the file in place. That keeps the host and the
pod looking at the same file, and the write is seen by avahi's directory
watch on the host.

If something on the host replaces the file instead of editing it in place
(many editors save by writing a new file and renaming it), the pod stays bound
to the old, deleted file and its writes stop reaching avahi. Restart the pod
after such an edit; see the edge cases below.

The container runs as root (`runAsUser: 0`) with every capability dropped,
which is enough to write a root-owned `0644` file and nothing more.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on controller shutdown |
| `--resync-period` | `10m` | Informer resync interval |
| `--verbose` | `false` | Verbose logging: per-service reconcile details and scan summary |
| `--debug` | `false` | Debug logging: same as verbose plus client-go internals (very noisy) |

## Edge cases

| Scenario | Handling |
|---|---|
| MetalLB reassigns IP | Update event → entry rewritten |
| Service deleted | Entry removed immediately |
| Node reboots, file wiped | Pod reschedules to same node; startup reconcile rewrites |
| Two Services claim same hostname | The Service with the lowest namespace/name wins deterministically; the other is skipped with a log error and Kubernetes Event |
| Annotation added to existing Service | Treated as new entry |
| `IP hostname` line longer than 255 bytes | Skipped with a log error and `HostsLineTooLong` Kubernetes Event; avahi-daemon cannot read longer lines |
| Annotation removed | Entry removed, same as delete |
| Pod restarts, file already correct | Hash match → file is not opened for writing, so avahi does not reload |
| avahi-daemon stopped or restarted | The controller keeps writing the file; avahi reads it when it starts |
| A static line above the block is invalid | avahi stops reading at that line, so managed entries are not published; fix the static line |
| Managed block markers are incomplete, reversed, or duplicated | Controller logs a reconciliation error and leaves the file untouched; repair the markers manually so static entries cannot be mistaken for managed content |
| **Manual edit** to hosts file using a rename-based editor (nano, vim, etc.) | Editor creates a new file inode; the pod's hostPath bind mount (which is a single-file mount) still points to the old inode. Subsequent controller writes silently go to the old inode and are invisible to the host. **Workaround**: restart the pod to re-bind to the current inode. **This is a known tradeoff** of mounting a single file via hostPath; mounting the parent directory instead would fix it but widens the volume scope. |

## Requirements

- `avahi-daemon` installed and running on the designated node (Linux builds watch `/etc/avahi` with inotify by default)
- Node labeled with `avahi-controller: "true"` for pod scheduling
- RBAC: `list/watch` on Services (cluster-wide), `create/patch` on Events

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
| `image.repository` | `ghcr.io/mm503/avahi-controller` | Container image repository |
| `image.tag` | *(chart appVersion)* | Image tag override |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `args` | `[--cleanup-on-exit=false]` | Controller flags |
| `nodeSelector` | `{avahi-controller: "true"}` | Node selector for scheduling |
| `tolerations` | `[{operator: Exists}]` | Tolerates any taint on the designated node |
| `resources.requests` | `cpu: 10m, memory: 32Mi` | Resource requests |
| `resources.limits` | `cpu: 50m, memory: 64Mi` | Resource limits |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |

The chart defaults to GHCR. To pull from Docker Hub instead, set
`--set image.repository=mm404/avahi-controller`.

Example — enable verbose logging and remove the managed block on shutdown:

```sh
helm install avahi-controller ./charts/avahi-controller \
  --namespace kube-system \
  --set args="{--cleanup-on-exit=true,--verbose}"
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

Release images are published to both registries, with tags `latest` and `x.y.z`:

```
ghcr.io/mm503/avahi-controller:<version>
docker.io/mm404/avahi-controller:<version>
```

Builds from feature branches go to a separate package, tagged
`dev-<version>-<branch>-<sha>`:

```
ghcr.io/mm503/avahi-controller-dev:dev-<version>-<branch>-<sha>
```
