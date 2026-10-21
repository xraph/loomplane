# Federation agent listener

You can run the agent control endpoint beside Loomplane's management API. It
serves enrollment, observations, signed policy snapshots and acknowledgments on
its own TLS listener. All federation switches default to `false`.

This is an infrastructure foundation. The [agent executable](federation-agent.md)
is available. The agent installer, service-target assignment, Kubernetes
deployment, gateway traffic enforcement and product routes are
still being built. Enabling the listener does not qualify a cloud provider or
make a service reachable across clouds.

## Configuration

Set these through `config.local.yaml` or the environment variables in
`config.yaml`:

```yaml
federation:
  enabled: true
  runtime:
    enabled: true
    listen_address: "127.0.0.1:9443"
    organization_id: "your-org-id"
    trust_domain: "loomplane.example"
    workload_api: "unix:///run/spire/agent.sock"
    signing_keys_file: "/run/secrets/federation-signing.json"
```

The listener needs an explicit IP address and port. Bind a reachable interface
when agents run on other machines, and preserve TLS all the way to Loomplane.
The runtime requires client certificates; management cookies and bearer tokens
cannot authenticate an agent.

Provision the control identity
`spiffe://loomplane.example/loomplane/control/org/your-org-id` through SPIRE before
startup. The local Workload API socket must deliver that exact identity and its
trust bundle. Loomplane follows credential rotation and verifies each agent's
current enrollment in PostgreSQL. An agent certificate alone grants no service
access.

Startup occurs after migrations and the shared event bus. Missing keys, an
unavailable identity source or a bind failure prevents startup. Health checks
report listener failure, invalid credentials and expired signing keys. Shutdown
cancels active requests and releases the listener and identity source.

## Policy signing keys

Supply a regular file readable only by its owner, for example mode `0600`.
The final path component must not be a symbolic link. Use this format:

```json
{
  "version": 1,
  "keys": [
    {
      "org_id": "your-org-id",
      "project_id": "your-project-id",
      "environment_id": "your-environment-id",
      "private_key": "<base64-encoded 64-byte Ed25519 private key>",
      "not_before": "<RFC3339 timestamp>",
      "not_after": "<RFC3339 timestamp>"
    }
  ]
}
```

The Ed25519 value contains the 32-byte seed followed by its 32-byte public key.
PEM and PKCS#8 encodings are not accepted. Keep this file outside source control.
Loomplane never prints its contents in validation errors or generates a fallback
key.

Each entry authorizes one exact organization, project and environment. All
entries must belong to the listener's organization, be currently valid and have
unique scopes. The file is limited to 256 KiB and 256 scopes. An unconfigured
scope cannot receive signed snapshots.

Keys are loaded at startup. Restart to replace the file, and provision matching
public verification keys to agents through trusted configuration before issuing
policy with a new key. Runtime snapshots cannot add their own trust keys. SPIRE
credential rotation is automatic; policy signing key replacement is currently
an explicit operation.

## Identity cleanup

You can enable enrollment cleanup independently of the agent listener:

```yaml
federation:
  enabled: true
  identity:
    enabled: true
    organization_id: "your-org-id"
    trust_domain: "loomplane.example"
    admin_socket: "unix:///run/spire/server.sock"
```

Use the corresponding `LOOMPLANE_FEDERATION_IDENTITY_*` variables from
`config.yaml` if you configure Loomplane through the environment. When both
identity cleanup and the runtime listener are enabled, their organization and
trust domain must match.

Mount the SPIRE server's local administration socket into Loomplane and restrict
access to the service account that runs it. This socket grants SPIRE administrative
authority. Keep it separate from the agent Workload API socket; TCP endpoints and
proxy-based connections are not supported. The client checks the server's trust
domain and administrative access at startup and before registration or deletion.
It accepts agent identities only in the configured organization. Cleanup inspects
each registration and refuses to delete control identities, foreign organizations,
administrators or downstream servers. These checks prevent accidental scope
mixups. They do not isolate Loomplane from another administrator who can modify
the same SPIRE server.

Cleanup starts after database migrations, runs an immediate sweep, then waits
30 seconds between sweeps. Each sweep processes at most 20 enrollments and has a
30-second deadline. Eligibility is rechecked under the enrollment row lock.
Failed deletions retain their cleanup references for retry; outstanding signed
policy leases also retain the enrollment until those leases expire.

Startup fails if the configured administration socket is unavailable. Health
checks cover worker liveness and the administration connection. Cleanup failures
are logged with the organization and attempted, deleted and failed counts.
A retained enrollment can still be waiting for a policy lease, so a failed sweep
does not by itself mark the worker unhealthy. Shutdown cancels the sweep, waits
for the worker and closes the administration connection.

This setting also wires the trusted Kubernetes installation and enrollment
services to the existing connection credential service. These are internal
control-plane operations; management routes, CLI commands and the installer
still need to expose and coordinate them.

## Trusted Kubernetes installation records

You can record an existing agent pod through `InstallationService.Record`. The
operation requires destination-management and trust-management permission. It
derives the organization, project, environment, connection and physical identity
from the destination, then reads the namespace, pod, node and SPIRE parent using
the connection's stored credential. It never installs a pod or changes cluster
permissions.

The request pins the pod name and UID, service account, container name and SPIRE
installation profile. The parent must be an active `k8s_psat` agent on the same
node, with a matching running SPIRE pod. The destination pod can be Running, or
Pending with a successful PodScheduled condition and a named node while it waits
for configuration. A Pending SPIRE parent is not accepted. SPIRE still attests
the actual container before it can acquire the requested identity.

Supply the current destination generation and the previous installation
generation, or zero for the first record. A successful write advances the
destination generation and assigns that value to the installation. Replacement
therefore invalidates pending enrollment handles and active agents from the old
generation. Recording an installation does not qualify a provider or approve
service access.

Enrollment requests contain only a destination ID. The controller loads the
saved installation, checks that its generation is current and re-reads the live
attestation evidence. It also requires a fresh supported workload-attestation
capability. Permission and destination identity are checked again after
attestation and around SPIRE issuance. If the final check fails, the handle stays
pending and its durable trust reference remains available for cleanup.

`InstallationService.Retire` removes the installation record and advances the
destination generation without needing a healthy provider. Existing enrollments
retain their trust references until cleanup succeeds and outstanding policy
leases expire. Installation changes and destination generation changes commit
with a scoped event in one transaction; management actions also enter the audit
trail. An installation or outstanding enrollment prevents destination removal.

The wired resolver currently enrolls consumer agents with no exported service
targets. Verified export assignment and launch-file delivery belong to the
installer work that follows. The launcher requires a regular configuration file,
so a Kubernetes projected volume needs controlled materialization before the
agent starts. Live Kubernetes pod attestation remains a separate qualification
gate.
