# Federation foundation checkpoint

Status: foundation contracts and blocked previews are implemented. Federation execution is unavailable. This checkpoint does not close Tasks 0 through 3 or authorize provider activation.

## What you can review

| Area          | Implemented                                                                                                                                                                               | Still required                                                                                                                                                       |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Qualification | Miget documentation inventory, live acceptance runbook and a pinned gateway CA-reload characterization                                                                                    | Live Miget evidence, immutable deployment, network/DNS replacement, billing cleanup, SAN verification, private gRPC, stream revocation and atomic policy replacement |
| Destinations  | Organization/project/environment scope, imported registration, get and capability refresh, physical identity pins, quarantine, optimistic updates, Warden permissions and Chronicle audit | List, unregister and re-enrollment lifecycle; public routes and product surfaces                                                                                     |
| Persistence   | Additive rerunnable PostgreSQL migration, scope constraints and destination/cleanup/trust connection retention                                                                            | Agent, endpoint and policy persistence, destination unregister rules that retain pending cleanup references                                                          |
| Manifest      | Strict version 2 federation intent, environment destination overrides, private exports, exact grant proposals, bounded selectors and preserved version 1 default                          | Cross-environment references; recipe, storage and protection sections from the shared version transition                                                             |
| Planning      | Deterministic network/policy previews, scoped observations, capability/identity revision pins and an unconditional execution barrier                                                      | Detailed cost, restart, regional and shared-resource impact assessment; executable approval and execution-time revalidation                                          |
| Reachability  | Pure evaluator for direct network evidence, DNS, workload identity, reviewed export/grant revisions, policy acknowledgment and leased ready endpoints                                     | Integration into executable planning, trusted observation producers and full path probes                                                                             |
| Runtime       | Legacy runner refuses network/policy actions before creating a deployment and again during resumed execution                                                                              | Agents, service discovery, workload attestation, private gateways, policy distribution and enforcement                                                               |

The internal management service is disabled by default through `federation.enabled`. Enabling that flag only permits in-process destination management. It cannot enable traffic or make a version 2 plan executable.

## Security decisions

- Importing a destination reads checked connection metadata. It has no provider mutation interface. Kubernetes identity uses the checked CA fingerprint and namespace UID; server identity uses the Docker daemon ID. Other providers remain unqualified.
- Missing, stale, changed or quarantined identity cannot provide usable capabilities. Registration and refresh advertise no supported runtime capabilities.
- A grant proposal is not approval. Approval includes the exact export contract and generation. Gateway acknowledgments must match both grant and export revisions.
- Current grants name workloads. A gateway certificate alone cannot authorize one. The manifest can describe a gateway identity profile, but that profile cannot satisfy the current workload grant evaluator.
- Only direct network proofs qualify. Independent tunnel edges do not establish safe multi-hop forwarding, prefix ownership or translation. Multi-hop support needs a separate path proof.
- A ready endpoint is an observation, not permission. Every layer must pass before the evaluator returns available. That result is currently a contract tested in isolation, not a live routing decision.
- Runtime calls do not become deployment prerequisites. Mutual service calls therefore do not create a grant-installation cycle.

## Verification

The review regression tests reproduced and then closed gateway/workload identity confusion, stale export approval and overlapping multi-hop acceptance. The pinned gateway characterization first loads a valid CA, removes its file, and records the current unsafe reload result without enabling that behavior in federation.

Focused Go tests cover destination isolation and identity changes, capability expiry, parser compatibility, comment preservation, planning revisions, reachability denial and runner rejection. PostgreSQL integration tests cover interrupted migration reruns, scoped destination persistence, optimistic updates and connection retention. The manifest parser completed 402,710 fuzz executions in a 30-second run.

Generated-client drift checks passed. The existing manifest API and generated clients now carry optional federation intent. Plan previews reuse existing response fields; no public destination management API is added. The proposed commit contents passed the full Go suite through a read-only Go build overlay. Builds, dashboard lint and the dashboard suite passed. The dashboard retry used two workers and passed all 232 tests after the default run hit two timeouts. Full lint initially reported nine issues in concurrent PostgreSQL/recovery work. After that work's separate commit, full `make lint` passed. Those concurrent changes are excluded from this checkpoint. A later shared-checkout `make test` encountered a new uncommitted recovery test referencing the not-yet-implemented `Service.Restore` and `RestoreInput`; that concurrent test is preserved and excluded from the proposed-commit verification.

## Next gates

1. Close G0 with an authorized Miget workspace, suitable credentials, an explicit cost envelope and a cleanup inventory. Preserve actual redacted captures separately from the documentation inventory.
2. Finish destination list/unregister and re-enrollment semantics, including active trust and pending cleanup references. Add duplicate-name isolation coverage across organizations and projects before exposing management routes.
3. Complete the remaining Task 3 review model and held-source behavior before adding executable network actions. Keep every version 2 plan blocked until trusted evidence and approval revalidation exist.
4. Proceed through Tasks 4 through 7 for endpoint leases, enrollment, signed policy, private HTTP enforcement and two-cluster acceptance. Do not substitute unit fixtures for G1 evidence.

See the [implementation plan](../superpowers/plans/2026-10-18-cross-cloud-federation.md), [Miget qualification](miget.md), [gateway qualification](federation-gateway.md) and [live acceptance runbook](../../tests/acceptance/federation/README.md).
