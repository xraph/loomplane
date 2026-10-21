# Federation agent listener

You can run the agent control endpoint beside Loomplane's management API. It
serves enrollment, observations, signed policy snapshots and acknowledgments on
its own TLS listener. Both federation switches default to `false`.

This is an infrastructure foundation. The agent installer, trusted installation
records, cleanup scheduling, gateway traffic enforcement and product routes are
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
