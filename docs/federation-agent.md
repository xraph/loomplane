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

The executable and trusted Kubernetes installation records are available.
Automated installation, launch-file delivery, verified service-target assignment,
Kubernetes deployment and gateway enforcement are still being built.
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
