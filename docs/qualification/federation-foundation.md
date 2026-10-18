# Federation foundation checkpoint

Status: foundation contracts and blocked previews are implemented. Federation execution is unavailable. Tasks 1 and 4 are implemented and locally verified, including destination lifecycle management and the runtime catalog. Task 0 and remaining Task 2/3 gates are open; provider activation is not authorized by this checkpoint.

## What you can review

| Area          | Implemented                                                                                                                                                                                                                                  | Still required                                                                                                                                                       |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Qualification | Miget documentation inventory, live acceptance runbook and a pinned gateway CA-reload characterization                                                                                                                                       | Live Miget evidence, immutable deployment, network/DNS replacement, billing cleanup, SAN verification, private gRPC, stream revocation and atomic policy replacement |
| Destinations  | Organization/project/environment scope, imported registration, scoped cursor listing, rename, guarded unregister, get and capability refresh, physical identity pins, quarantine, optimistic updates, Warden permissions and Chronicle audit | Agent enrollment/revocation lifecycle; public routes and product surfaces                                                                                            |
| Persistence   | Additive rerunnable PostgreSQL migration, scope constraints and destination/cleanup/trust connection retention                                                                                                                               | Policy persistence; runtime registration retains destinations, and later controllers must retain and release their references                                        |
| Manifest      | Strict version 2 federation intent, environment destination overrides, private exports, exact grant proposals, bounded selectors and preserved version 1 default                                                                             | Cross-environment references; recipe, storage and protection sections from the shared version transition                                                             |
| Planning      | Deterministic network/policy previews, scoped observations, capability/identity revision pins and an unconditional execution barrier                                                                                                         | Detailed cost, restart, regional and shared-resource impact assessment; executable approval and execution-time revalidation                                          |
| Reachability  | Pure evaluator for direct network evidence, DNS, workload identity, reviewed export/grant revisions, policy acknowledgment and leased ready endpoints                                                                                        | Integration into executable planning, trusted observation producers and full path probes                                                                             |
| Runtime       | Legacy runner refuses network/policy actions before creating a deployment and again during resumed execution                                                                                                                                 | Agents, service discovery, workload attestation, private gateways, policy distribution and enforcement                                                               |

The internal management service is disabled by default through `federation.enabled`. Enabling that flag only permits in-process destination management. It cannot enable traffic or make a version 2 plan executable.

## Security decisions

- Importing a destination reads checked connection metadata. It has no provider mutation interface. Kubernetes identity uses the checked CA fingerprint and namespace UID; server identity uses the Docker daemon ID. Other providers remain unqualified.
- Unregister removes an unused local alias only. Active destination references block removal atomically; pending connection cleanup and trust references survive alias deletion. Replacing a physical destination requires a new destination ID.
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

## Runtime catalog checkpoint

You can exercise the catalog through its internal Go interfaces. It persists scoped observations in PostgreSQL, assigns 30-second leases, rejects older generations and retains deletion tombstones until cached snapshots expire. The stored observer sequence continues to reject replay after tombstone cleanup.

Pagination reads one database revision. Each page checks the requesting agent, current source registrations and the exact permitted export scopes. A policy change, shorter authorization lifetime, destination quarantine or expired lease invalidates the cursor. A reconnect starts with a complete snapshot; the existing realtime bus supplies bounded wakeups without exposing endpoint payloads.

The Kubernetes observer uses the pinned discovery/v1 client contract. It lists before watching, resumes from the list resource version, relists after watch closure or HTTP 410, and renews only after a fresh API read. Namespace, Service UID, registered port and address ranges must match. Terminating endpoints drain, unready or non-serving endpoints stay unavailable, and duplicate conflicting slice records close observation until corrected.

Bounds are explicit: 1,000 observations per batch, 10,000 retained records per observer, 10,000 records per snapshot, 100 records per page and four persisted cursors per agent. Known metadata endpoints are rejected even inside an allowed range, including the [AWS IPv6 metadata address](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html). Metrics contain operation and outcome labels only.

Runtime catalog race tests and real PostgreSQL restart, revocation, bounded cursor and migration rerun tests passed. Full `make test` passed with 235 dashboard tests, and `make build` passed. The final Go lint command used `--allow-serial-runners --max-same-issues=0` to wait for concurrent lint jobs; it reported zero issues. Dashboard typechecking and lint passed. The exact proposed Go contents also passed `go test ./...` through a read-only overlay. Formatting used scoped `gofmt`, `goimports -local github.com/xraph/loomplane` and Prettier because this repository has no `make f` target.

Catalog snapshots are unsigned input to the later policy signer. No public observation route, workload attestation, agent enrollment, private data plane or live Kubernetes acceptance is enabled by this checkpoint. Task 5 must pin namespace identity and persist sequence reservations before wiring the observer into an authenticated agent.

## Next gates

1. Close G0 with an authorized Miget workspace, suitable credentials, an explicit cost envelope and a cleanup inventory. Preserve actual redacted captures separately from the documentation inventory.
2. Connect agent, export, network and policy controllers to the restrictive destination reference table as those controllers are implemented. Destination listing, rename, unregister and fresh-ID replacement registration now have unit and PostgreSQL coverage, including duplicate-name scope isolation and a concurrent reference/unregister race.
3. Complete the remaining Task 3 review model and held-source behavior before adding executable network actions. Keep every version 2 plan blocked until trusted evidence and approval revalidation exist.
4. Proceed through Tasks 5 through 7 for enrollment, signed policy, private HTTP enforcement and two-cluster acceptance. Unit and PostgreSQL fixtures do not close the live G1 gate.

See the [implementation plan](../superpowers/plans/2026-10-18-cross-cloud-federation.md), [Miget qualification](miget.md), [gateway qualification](federation-gateway.md) and [live acceptance runbook](../../tests/acceptance/federation/README.md).
