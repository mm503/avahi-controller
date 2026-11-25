# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change by itself; see [How avahi picks up changes](#how-avahi-picks-up-changes).

The controller does not speak mDNS. avahi-daemon on the node answers the queries, so names keep resolving while the pod is restarting or being upgraded; only changes wait for it. Clients need nothing beyond the mDNS support that macOS, Windows, iOS, current Android and most Linux desktops already have, and nothing has to be configured on the router or a DNS server.

It is meant for a load balancer that hands out dedicated LAN addresses, such as MetalLB, kube-vip or Cilium LB-IPAM. It does not work with addresses that belong to a node, which is what the ServiceLB bundled with k3s assigns; see [Limitations](#limitations).

Please note, this is not a production tool. mDNS is not a production-level solution. This is for a home lab and simple Kubernetes clusters.

## Annotation

Add to any `type: LoadBalancer` Service:

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

The controller ignores Services without this annotation, and requeues with backoff if a LoadBalancer IP hasn't been assigned yet.

Use a single label followed by `.local`, such as `myapp.local`. A name outside `.local` is written to the file with a warning, but mDNS clients do not look it up. A deeper name such as `myapp.lab.local` is published too, but Linux clients that use `nss-mdns` with its default settings reject `.local` names with more than two labels.

## Limitations

- **One node.** Only the designated node answers. If that node or its avahi-daemon is down, the names stop resolving as client caches expire (avahi publishes them with a 120-second TTL), even when the load balancer has moved the IP to another node. See [More than one node](#more-than-one-node).
- **One hostname per IP address.** avahi publishes a reverse record (address to name) for every line in its hosts file and allows one per address. When Services with different hostnames share an IP (MetalLB `allow-shared-ip`), the controller publishes the hostname of the Service with the lowest namespace/name and skips the others with a `SharedIPConflict` Event. Services that share an IP and carry the same hostname are fine. For the same reason there is one hostname per Service and no aliases, so many names in front of one ingress controller IP are not possible.
- **Not a node's own address.** avahi already publishes each node address under the node's hostname, so an entry for that address is rejected (`Local name collision` on that node, `Host name conflict` when another node running avahi owns it). This rules out k3s ServiceLB (klipper-lb) and any setup where the LoadBalancer IP is a node IP. The controller raises a `NodeAddress` Event when the IP is an address of the designated node; it cannot tell for other nodes.
- **One address per Service.** The first IP in `status.loadBalancer.ingress` is used. A dual-stack Service gets a record for that address only.
- **One network segment.** mDNS is link-local multicast. Clients on another VLAN or subnet, or behind a VPN, do not see the names unless something reflects mDNS between the segments.
- **avahi-daemon on the host.** The node needs avahi-daemon installed with its configuration in `/etc/avahi`. Immutable node operating systems that cannot install host packages are not supported.

### When to use something else

- Many hostnames on one IP, Ingress hostnames, or nodes without avahi-daemon: [external-mdns](https://github.com/blake/external-mdns) answers mDNS itself instead of going through avahi and publishes Ingress hosts as well as LoadBalancer Services.
- Names that must resolve across VLANs, outside `.local`, or for clients without mDNS: use real DNS. [external-dns](https://github.com/kubernetes-sigs/external-dns) can update a DNS server you already run, and [k8s_gateway](https://github.com/k8s-gateway/k8s_gateway) serves the names from CoreDNS inside the cluster. Both need a DNS server or a forwarding rule on the LAN, which is the setup step this controller avoids.

## More than one node

The chart runs one pod, and a pod can write only the hosts file of the node it is on, so one node publishes every name. `replicaCount` above 1 is rejected by the chart, and a second replica on the same node would only write the same file twice.

Pinning to one node is a limit of this deployment, not of avahi or mDNS. The records point at the load balancer IP, not at the node, so any node can answer for them. Tested with avahi 0.8, three daemons on one segment:

- Two nodes publishing the same name with the same address both establish it. Neither sees a conflict.
- When one of them stops, the other keeps answering.
- Two nodes publishing the same name with *different* addresses do conflict. The node that tries to publish the new address while another still holds the old one logs `Host name conflict for "myapp.local", not established.` and stays silent for that name until its avahi-daemon reloads. This is what happens when a Service's IP changes and the nodes do not update at the same instant.

Running one controller pod on every node that has avahi-daemon is therefore possible, and it is not implemented yet. It needs a DaemonSet in the chart and a second reload a few seconds after each change so that a node that lost the race publishes again. `PROJECT.md` has the details.

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

The pod also reads two environment variables, both set by the chart and the raw manifests: `NODE_NAME` (recorded as the source host of Events) and `HOST_IP` (the node's address, used only for the `NodeAddress` warning). Both are optional. If you override `env` in your values, copy them from the chart's `values.yaml`, because Helm replaces the list.

## Edge cases

| Scenario | Handling |
|---|---|
| MetalLB reassigns IP | Update event → entry rewritten |
| Service deleted | Entry removed immediately |
| Node reboots, file wiped | Pod reschedules to same node; startup reconcile rewrites |
| Two Services claim same hostname | The Service with the lowest namespace/name wins deterministically; the other is skipped with a log error and Kubernetes Event |
| Annotation added to existing Service | Treated as new entry |
| `IP hostname` line longer than 255 bytes | Skipped with a log error and `HostsLineTooLong` Kubernetes Event; avahi-daemon cannot read longer lines |
| Services with different hostnames share one LoadBalancer IP | avahi publishes one hostname per address, so the Service with the lowest namespace/name wins; the others are skipped with a log error and `SharedIPConflict` Kubernetes Event |
| Services with the same hostname share one LoadBalancer IP | One line is written; no conflict is reported |
| LoadBalancer IP is the designated node's own address | The line is written, but avahi normally rejects it because the address already belongs to the node's hostname. The controller logs a warning and emits a `NodeAddress` Kubernetes Event once |
| Annotation removed | Entry removed, same as delete |
| Pod restarts, file already correct | Hash match → file is not opened for writing, so avahi does not reload |
| avahi-daemon stopped or restarted | The controller keeps writing the file; avahi reads it when it starts |
| A static line above the block is invalid | avahi stops reading at that line, so managed entries are not published; fix the static line |
| Managed block markers are incomplete, reversed, or duplicated | Controller logs a reconciliation error and leaves the file untouched; repair the markers manually so static entries cannot be mistaken for managed content |
| **Manual edit** to hosts file using a rename-based editor (nano, vim, etc.) | Editor creates a new file inode; the pod's hostPath bind mount (which is a single-file mount) still points to the old inode. Subsequent controller writes silently go to the old inode and are invisible to the host. **Workaround**: restart the pod to re-bind to the current inode. **This is a known tradeoff** of mounting a single file via hostPath; mounting the parent directory instead would fix it but widens the volume scope. |

## Requirements

- `avahi-daemon` installed and running on the designated node (Linux builds watch `/etc/avahi` with inotify by default). If `/etc/avahi` does not exist, the pod cannot mount the hosts file and does not start
- A load balancer that assigns dedicated LAN addresses (MetalLB, kube-vip, Cilium LB-IPAM), one per published hostname
- Clients on the same network segment as the designated node
- Node labeled with `avahi-controller: "true"` for pod scheduling
- RBAC: `list/watch` on Services (cluster-wide), `create/patch` on Events

## Deployment

### Label the node

This is needed even on a single-node cluster (such as k3s and similar). Without the label, the pod's `nodeSelector` will not match any node and the pod will remain Pending. The controller writes the hosts file of the node it runs on, so it is pinned to the node where `avahi-daemon` is running and must not move to a node without it.

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
| `terminationGracePeriodSeconds` | `30` | Grace period for pod shutdown |

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

The chart is released together with the controller and has the same version
number. A breaking change in either bumps the major version.

#### Upgrading from 0.6.x

The `--reload` and `--avahi-service` flags were removed, along with the
host D-Bus socket mount (avahi-daemon already reloads the hosts file on its
own; see [How avahi picks up changes](#how-avahi-picks-up-changes)). Before
upgrading:

- Remove `--reload` and `--avahi-service` from your `args`. The controller
  now exits at startup if either is passed.
- If you set your own `volumes`, `volumeMounts` or `env`, delete the
  `/run/dbus/system_bus_socket` volume and mount and the
  `DBUS_SYSTEM_BUS_ADDRESS` variable. Helm replaces these lists instead of
  merging them, so a custom copy keeps mounting the host D-Bus socket, which
  gives the root container control of systemd on the node.
- For raw manifests, re-apply `deploy/`.

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
