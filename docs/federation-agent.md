# Destination agent

Build and start the agent with a configuration issued for that installation:

```sh
make build-agent
bin/loomplane-agent --config /etc/loomplane-agent/agent.json
```

`--help` and `--version` work without credentials. The process opens no inbound
ports. It uses the local SPIRE Workload API, authenticates the control plane,
consumes one enrollment, observes registered services and applies signed policy
before acknowledging it.

The executable, trusted Kubernetes installation records and Kubernetes launch
builder are available. The builder creates pod definitions and delivers immutable
configuration to an exact scheduled pod. Automated installation and restart
coordination, verified service-target assignment and gateway enforcement are still
being built. The opt-in launch service is registered in the application, but its
management route and automatic worker are still pending.
An acknowledgment currently proves installation in the agent's local policy
state, not that a remote service is reachable through a gateway.

## Installation configuration

The JSON file contains public identity, enrollment and verification material.
It must be a regular file at an absolute path, with no group/world write
permissions. The final path component cannot be a symlink. Unknown fields,
trailing JSON values and files larger than 1 MiB are rejected.

| Field | Value supplied by the installer |
| --- | --- |
| `version` | `1` |
| `control_endpoint` | HTTPS origin of the separate agent listener, including its port |
| `workload_api` | Canonical local socket address, such as `unix:///run/spire/agent.sock` |
| `state_path` | Absolute path to the agent's SQLite file in a private directory |
| `enrollment_id` | One-use UUID returned by the enrollment controller |
| `enrollment_expires_at` | The controller's enrollment expiry, within its five-minute window |
| `registration` | Exact agent identity, destination generation and export bounds from the enrollment record |
| `namespace` | Kubernetes namespace selected for this installation |
| `targets` | Registered export IDs with their exact Service names, UIDs and port names |
| `trust_generation` | Nonzero generation of the trusted public signing-key configuration |
| `signing_keys` | Public Ed25519 keys with `public_key`, `not_before` and `not_after` |

`registration.agent` contains `org_id`, `project_id`, `environment_id`,
`destination_id`, `agent_id`, `epoch` and the matching SPIFFE `uri`.
`registration` also contains `destination_generation`, `active` and `exports`.
Each export's bounds include its scope, destination, placement, binding, port
and allowed CIDRs. The server checks these against its durable registration;
editing the local file cannot widen server authority.

Each target uses `export_id`, `service_name`, `service_uid` and `port_name`.
Supply exactly one target for every registered export. A target's Service UID
must come from the actual Kubernetes Service, not its name alone.

`public_key` is the base64 encoding of a 32-byte Ed25519 public key. It must
match the control plane's scoped signing key. Never put private signing keys,
management API tokens or kubeconfigs in this file. Key replacement currently
requires trusted configuration delivery and a new process enrollment.

## Kubernetes access

For an installation with exports, the agent uses only the in-cluster service
account credential. It does not read `KUBECONFIG`, run credential plugins,
follow redirects or use an ambient HTTP proxy. It lists and watches
`discovery.k8s.io/v1` EndpointSlices in the configured namespace. Give that
service account a namespace Role with `list` and `watch` on `endpointslices`.
The observer verifies namespace, Service ownership, UID, port and registered
address bounds on every update.

The enrollment controller separately needs permission to inspect namespace,
pod, node and SPIRE parent evidence. Those permissions do not belong to the
runtime observer's service account.

A consumer-only installation has no exports and an empty target list. It
publishes empty observation heartbeats and receives policy without opening a
Kubernetes client or acquiring its credential.

## Kubernetes launch resources

[The agent manifest](../deployments/federation/agent.yaml) shows an exporting
installation. Replace its example namespace, attempt UUID and image digest before
use. It does not include a launch ConfigMap: the trusted controller must verify the
scheduled pod and issue an enrollment before delivering that file. Applying this
manifest alone leaves the pod waiting for configuration and does not grant service
access.

The `internal/agentinstall` builder produces the same resources for the controller.
Use a new attempt UUID for every process. Create its ServiceAccount first, then its
optional observer Role and RoleBinding, then its Pod. Keep the resource UIDs so
recovery can delete only the objects that attempt owns. Do not adopt or overwrite
resources already using those names.

The pod runs as UID/GID 10001 with a read-only root filesystem, no Linux
capabilities, no privilege escalation and the runtime's default seccomp profile.
It opens no listener. The cluster must already provide the `csi.spiffe.io` driver
and a SPIRE socket named `agent.sock`; the pod mounts that socket through CSI.
SPIRE's node and workload attestors still need separate installation and
qualification.

Exporters receive a projected, rotating Kubernetes API token and namespace-local
EndpointSlice `list`/`watch` permission. Consumers receive neither. Both disable
automatic token mounts. The state volume contains a private child directory that
the agent creates with mode 0700; its parent volume is writable by the pod group.

After the pod reaches scheduled Pending, the controller records its verified
installation, issues enrollment and loads the ready record from storage. Delivery
requires the already verified destination connection and a guard that rechecks
management permission, installation generation and enrollment state before the
write. Scope, epoch and export bounds come from that record. Public signing keys
and the listener address come from control-plane configuration; Service targets
must come from verified assignments.

Delivery checks the namespace UID, pod UID, service account, image, command,
mounts and security settings. Injected sidecars and changed launch resources fail
validation. The builder creates one immutable ConfigMap owned by that pod UID,
then mounts `agent.json` using `subPath`. This supplies a regular, read-only file
without loosening the agent's symlink checks. Data is public; no management token
or private signing key belongs in the ConfigMap.

A delivery timeout may mean Kubernetes accepted the write. Inspect that exact
attempt and retire its enrollment before replacing it. Do not retry enrollment
or change the launch file in place. `restartPolicy: Never` prevents a container
restart from silently reusing its consumed handle. The launch service can recover
matching configuration writes; automatic replacement is still pending.

Regenerate the example after changing the shared builder:

```sh
LOOMPLANE_UPDATE_AGENT_MANIFEST=1 go test ./internal/agentinstall -run TestKubernetesExampleManifest -count=1
```

For the optional Kubernetes API test, supply a self-contained kubeconfig for a
disposable cluster. The test creates its own namespace and deletes it by UID.
It checks real scheduling, resource defaulting, immutable delivery and RBAC. It
does not install CSI or SPIRE, start the agent or qualify workload identity.

```sh
LOOMPLANE_AGENT_INSTALL_TEST_KUBECONFIG=/absolute/path/to/test-kubeconfig \
  go test -race -tags=integration ./internal/agentinstall -run TestKubernetesLaunchAPI -count=1
```

## Guarded launch delivery

Enable the internal launch service only after configuring identity administration
and the agent listener:

| Environment variable | Required value |
| --- | --- |
| `LOOMPLANE_FEDERATION_LAUNCH_ENABLED` | `true` (default: `false`) |
| `LOOMPLANE_FEDERATION_LAUNCH_CONTROL_ENDPOINT` | Public HTTPS origin of the separate agent listener |
| `LOOMPLANE_FEDERATION_LAUNCH_AGENT_IMAGE` | Agent image pinned by SHA-256 digest |
| `LOOMPLANE_FEDERATION_LAUNCH_TRUST_GENERATION` | Positive generation for the public signing trust |

The launch service uses destination, enrollment and attempt IDs. You cannot pass
identity selectors, credentials, service targets or signing keys through its
request. It checks destination and trust-management permission, current provider
qualification, the saved installation and a ready enrollment. It then verifies
live Kubernetes/SPIRE evidence again before writing through the destination's
explicit connection. The management Kubernetes client refuses redirects, keeping
credentialed requests on the selected cluster endpoint.

The listener supplies a copy of its loaded public signing key for the exact
organization, project and environment. Delivery fails while that listener is
stopped or unhealthy, or when its scope has no signing key. Editing the key file
without restarting the listener does not change delivered trust.

Enrollment reads, bootstrap authentication and consumption share the same ready,
expiry, destination and latest-epoch checks. Delivery rechecks those records and
human authority after provider reads. A newer enrollment fences an older pending
handle immediately. A consumed handle cannot deliver another launch file.

Before the Kubernetes write, the service saves the attempt, enrollment,
destination generation, pod UID and immutable configuration digest in PostgreSQL.
Only one unresolved delivery is allowed per destination, and an enrollment cannot
be assigned to another delivery attempt. The pending record prevents destination
deletion while the outcome remains unknown.

If the write response is lost, use the internal service's `Recover` operation with
the destination and attempt IDs. It checks your destination and trust-management
permissions, the current installation, cluster identity, namespace UID, pod UID
and configuration digest. Recovery only reads Kubernetes. A matching result saves
the ConfigMap UID and releases the pending delivery reference, even if the agent
has already consumed its enrollment. Confirmation is delivery history; it does
not renew the agent's authority. Both reservation and confirmation append project
events in the same transaction as the saved record.

Missing resources, changed identities and mismatched content leave the attempt
pending. Repeating delivery does not send another create request. Use the internal
retirement service to revoke authority and remove the attempt's pod and configuration
before preparing a replacement.

The application currently wires consumer installations only. Exporting agents
still need verified service-target assignments. Automatic resource preparation,
replacement, management API, CLI and dashboard controls are pending.

## Retiring a launch attempt

Call the internal retirement service with the destination and attempt IDs. It saves
`retiring` before revoking enrollment, and blocks new delivery while cleanup is
pending. Revocation deactivates the observer first. SPIRE deletion and any retained
policy leases must finish before cleanup can release its destination reference.

Cleanup checks the current cluster and namespace identity, then targets the saved
pod UID and exact configuration digest. Delete requests include UID and resource
version preconditions. A replacement pod with the same name survives. Changed
configuration, revoked permission, a finalizer or a lost delete response leaves the
attempt in `retiring`; retrying inspects the resources again without issuing an
enrollment or creating resources. Retirement records project events and Chronicle
audit entries.

The service marks the attempt `retired` only after the original pod and configuration
are observed absent and the enrollment is no longer retained. Its history keeps the
original IDs. A newer installation generation on the same physical destination does
not prevent cleanup of the old pod UID.

Service accounts, RBAC and the trusted installation record remain separate. This
cleanup path has no recorded creation UIDs for those Kubernetes objects, so it
preserves them. Automatic resource preparation and replacement, background retries,
and management API, CLI and dashboard controls are still pending. Replacement must
use a new pod identity and fresh enrollment.

## Shutdown and restart

SIGTERM and SIGINT stop observation and watch loops, close local policy, then
release network clients, identity sources and SQLite. Persisted state records
epochs and observation sequence numbers, not permission to resume traffic.

Every process requires a fresh enrollment. Reusing a consumed enrollment or an
old epoch fails, including after restoring a state directory. The installer
must provision a new server-issued epoch before restarting an installation.
Do not build an automatic retry loop around the enrollment handshake: a lost
response leaves its consumption status uncertain.

See [the control-plane listener configuration](federation-runtime.md) for SPIRE
identity and policy signing-key setup.
