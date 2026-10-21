# Federation acceptance

G0 is open. Offline gateway characterization and documented Miget constraints do not prove private cross-cloud communication.

Before live probes, record the test workspace/account, two destination identities, disposable resource inventory, spending limit, expiry and cleanup owner. Use explicit resource IDs for cleanup. Never delete resources by a broad name prefix.

1. Capture scoped reads and denials with workspace credentials. Prove pagination and exact resource identity.
2. Deploy an immutable application revision with provider automation disabled; replace it and verify readiness and log redaction.
3. Try the same credential from a neighboring application. Prove private backend denial over IPv4, IPv6 and public ports.
4. Resolve the service from both networks before and after replacement. Record addresses, TTL, readiness and any required restarts.
5. Import an existing network path and probe each direction. A working tunnel grants no service access.
6. Exercise grant expiry, revocation, restart and control-plane outage with long-lived streams. Retain denial evidence.
7. Remove owned resources, verify absence and release gateway addresses separately. Record any unresolved cleanup reference.

For each check, retain the command, sanitized output, timestamp and result (`supported`, `unsupported` or `unverified`). Fixtures must state whether they came from live capture or synthetic input. Never promote documentation-only observations to supported capabilities.

## Local Kubernetes identity

You can exercise the prepared-agent path without cloud credentials:

```sh
python3 -m unittest discover -s tests/acceptance/federation/kubernetes -p 'test_*.py'
python3 tests/acceptance/federation/kubernetes/run.py
```

You'll need Go, Docker, kind and kubectl. The runner creates a uniquely named
single-node cluster with pinned SPIRE, SPIFFE CSI and PostgreSQL images. Every
Kubernetes command uses that cluster's explicit kubeconfig. On exit, it removes
the cluster and its own application image, including after a failed check.

The fixture calls the production preparation, installation, enrollment, launch
and retirement services against real PostgreSQL and Kubernetes. It checks PSAT
node attestation, CSI socket delivery, signed-policy acknowledgment, recovery
after bootstrap consumption, and denial for a neighboring pod using the same
service account. The control process shares the SPIRE server's local admin socket;
the destination agent receives only the Workload API socket through CSI. Kubelet
TLS remains verified against the certificate read from the disposable node.

Connection metadata and management authorization are fixture inputs. This run
doesn't qualify their management UI, Miget, workload replacement, or private
traffic between two clusters. A normal Go test run skips the live probes; use the
runner above to execute them.

The node and CSI setup follows the pinned upstream
[PSAT configuration](https://github.com/spiffe/spire/blob/v1.15.4/doc/plugin_server_nodeattestor_k8s_psat.md)
and [CSI example](https://github.com/spiffe/spiffe-csi/tree/v0.2.13/example/config).
