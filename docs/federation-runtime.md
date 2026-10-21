# Federation agent listener

You can run the agent control endpoint beside Loomplane's management API. It
serves enrollment, observations, signed policy snapshots and acknowledgments on
its own TLS listener. All federation switches default to `false`.

This is an infrastructure foundation. The [agent executable](federation-agent.md)
is available. The agent installer, trusted installation
records, gateway traffic enforcement and product routes are
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

This setting runs cleanup for existing enrollment records. Trusted installation
records and the controller that requests new enrollments are still being built.
