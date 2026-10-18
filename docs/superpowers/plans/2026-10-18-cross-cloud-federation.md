# Cross-cloud Federation Implementation Plan

Implementation checkpoint: [foundation contracts and blocked previews](../../qualification/federation-foundation.md). Task 1 is implemented and verified. Task 0 and the remaining Task 2/3 gates stay open as listed in the checkpoint.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy and operate private services across independent clouds and clusters, with stable discovery, enforced communication grants and a verified Miget integration.

**Architecture:** Extend Loomplane's existing planner, Dispatch workflows and provider executors with scoped destinations, a runtime catalog, network drivers and versioned gateway policy. Destination agents observe local workloads and apply approved snapshots. Provider and Kubernetes controllers retain ownership of local workload lifecycle.

**Tech Stack:** Existing Go/Forge, Grove/Postgres, Dispatch, Ctrl Plane, Authsome, Warden, Chronicle, Vault, React/TanStack and generated Forge clients. Add SPIFFE/SPIRE integration, destination-local DNS/proxies and Miget REST APIs only behind verified capability contracts. Reuse the pinned Bastion gateway where its tested interfaces meet the design.

**Spec:** [Architecture, current-state audit and security contract](../specs/2026-10-18-cross-cloud-federation-design.md). Read it before implementing a task.

## Global constraints

- Work directly on `main` in the primary checkout. Preserve concurrent changes and commit only owned paths. Do not create branches or worktrees. Push verified commits; do not merge.
- Keep version 1 manifests, current plugins and active `deployment-v2` workflows compatible. Version 2 is explicit and gated.
- Preserve the existing source lockset, connection ownership, Vault secret storage, Warden checks and Chronicle audit path. Never put credentials in manifests, plan JSON, endpoint URLs or logs.
- Planning is pure: it reads persisted capability snapshots and never calls providers. Execution rechecks authority and relevant capabilities before mutation.
- Local controllers own local runtime reconciliation. Access grants and network changes require approved configuration. Health observation cannot expand authority.
- `needs` supplies dependency/configuration intent; it grants no runtime access. Service exports grant no access either.
- Initial federation is within one organization. Cross-organization activation remains disabled until Task 17 passes.
- Proposed initial defaults: 10-second observation, 30-second endpoint lease, DNS TTL at most 5 seconds, three successful readmission probes, 30-second normal drain, five-minute policy lease renewed every minute, one-hour maximum workload credential validity renewed before half-life, and stale agent status after 30 seconds.
- Unproven identity, immutable artifact, endpoint freshness or backend isolation blocks the affected capability. No public-endpoint fallback for a private dependency.
- HTTP non-idempotent retries default to zero. TCP streams are never replayed. An application rollback never restores revoked permissions.
- Use the shared ZeroState and generated clients. Verify desktop at 1440px and narrow layout at 390px. Do not add a tour without a registered walkthrough and working anchors.
- Read rex-voice and apply humanizer in embedded mode to repository prose and commit bodies. No em dashes or co-author trailers.

## Review focus

1. Duplicate names across organizations, projects or destinations must not resolve to another tenant's endpoint. Tasks 1, 4 and 17 own those tests.
2. A stale, restored or disconnected agent must not resurrect a revoked grant or deleted endpoint. Tasks 4, 5 and 6 exercise generations, expiry and restore quarantine.
3. A timed-out Miget create request may have succeeded and incurred cost. Tasks 9 and 11 must reconcile ownership before retrying it.
4. A rolling deployment can succeed while provider DNS still points at a retired address. Tasks 10, 12 and 16 verify discovery from both networks after replacement.
5. A network path can bypass the proxy enforcing policy, including through IPv6 or an old public port. Tasks 7, 8 and 16 test direct-backend denial and exposure removal.

## Delivery order and completion gates

This is one program with separately reviewable workstreams. Complete each gate before enabling its capabilities. You can prepare fixtures and provider client code independently, but do not activate a later stage against an incomplete security contract.

| Stage                       | Tasks | Working deliverable                                                | Gate                               |
| --------------------------- | ----- | ------------------------------------------------------------------ | ---------------------------------- |
| Qualification               | 0     | Reproducible contracts and explicit provider blockers              | G0                                 |
| Shared foundation           | 1-3   | Destinations, version 2 intent, approved network/access plans      | Migration and compatibility checks |
| Private two-cluster slice   | 4-7   | Runtime discovery, identity, policy and private HTTP calls         | G1                                 |
| Protocol/runtime expansion  | 8     | Server, gRPC and TCP profiles; managed-provider restrictions       | G2                                 |
| Miget lifecycle             | 9-10  | Connect, deploy, observe, replace and remove Miget applications    | G3                                 |
| Miget networking            | 11-12 | Imported and then managed links, stable discovery and secure calls | G4                                 |
| Multi-placement operations  | 13-14 | Stateless rollout, failover and durable recovery                   | G5                                 |
| Product and qualification   | 15-16 | Complete API/CLI/dashboard flow and live evidence                  | G6                                 |
| Optional partner federation | 17    | Bilateral access across organizations                              | Separate opt-in release            |

Critical path: `0 -> 1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8`; Miget work follows `0 + 1 -> 9 -> 10 -> 11`, then `7 + 8 + 10 + 11 -> 12`. Multi-placement follows `7 -> 13 -> 14`. Product work can begin against stable API contracts after Task 3 and closes after Task 14. Task 16 follows all required tasks; Task 17 is optional and disabled by default.

Do not estimate a release date before G0. Miget's immutable deployment, identity, DNS and two-sided tunnel setup can change the implementation scope. Track completed tests and unresolved gates, not an aggregate percentage.

## File and contract map

All paths below are relative to the repository root. Files under new directories are proposed. Do not rename existing repository analysis packages to make room for runtime discovery.

| Area             | Files and responsibility                                                                                                                                         |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Shared contracts | New `pkg/federation/{types,capabilities,snapshot}.go`: portable scope, destination, export, grant, endpoint and snapshot types.                                  |
| Management       | New `internal/federation/{service,store,pgstore,enrollment}.go` and `extensions/federation/{extension,routes}.go`: authorized CRUD, observations and wiring.     |
| Migrations       | New `database/migrations/federation.go`, `federation_placements.go`; register through `migrations.go`, extend `connection_uses.go`.                              |
| Planning         | New `pkg/manifest/federation.go`, `internal/planning/{federation,reachability,placements}.go`; update schema, editor, planner and binding store.                 |
| Catalog          | New `internal/runtimecatalog/{catalog,pgstore,watch,kubernetes,server,managed,dns}.go`: observations, leases and filtered snapshots.                             |
| Networking       | New `internal/networking/{driver,service,ipsec,imported,miget}.go`: reachability evidence, link ownership and durable operations.                                |
| Identity         | New `internal/workloadidentity/{issuer,spiffe,enrollment,trust}.go`: issuer integration and attestation; no new CA implementation.                               |
| Policy           | New `internal/access/{policy,compiler,snapshot,revocation}.go`: effective grants and enforcement configuration.                                                  |
| Local execution  | New `cmd/loomplane-agent/main.go`, `internal/agent/{agent,watch,apply,probe,store}.go`; new `internal/mesh/{gateway,identity,routes,tcp}.go`.                    |
| Miget            | New `internal/clouds/miget/{client,apps,deployments,networks,webhooks,logs}.go`, `internal/connections/miget.go`, `internal/deployments/miget.go`.               |
| Lifecycle        | New `internal/deployments/{federation,placements,cleanup}.go`, `internal/orchestration/federation.go`; keep runner changes small and call helpers.               |
| Product          | New `apps/dashboard/src/components/federation/` and CLI handlers in `internal/ctl/federation.go`; extend existing Infrastructure, Integrations and Deploy views. |
| Verification     | New `tests/acceptance/federation/`; integration tests beside existing deployment, connection and security tests.                                                 |

Task 1 defines these reusable types in `pkg/federation`: `Scope`, `Destination`, `CapabilitySet`, `NetworkLink`, `ServiceExport`, `AccessGrant`, `Endpoint`, `ObservationBatch`, `Snapshot`, `SnapshotRequest`, `Acknowledgment`, `EnrollmentRequest`, `Enrollment`, `AgentIdentity`, `ProbeRequest`, `ProbeResult`, `Placement`, `CleanupOperation`, `PolicyInput` and `PolicyBundle`. Their fields follow the spec's domain table. Every persisted object has immutable ID, scope, generation and timestamps. Use typed enums for ownership, protocol, identity profile and readiness; avoid unvalidated `map[string]any` at trust boundaries.

## Verification and commit procedure

Every task below ends with this procedure after its focused tests pass:

1. Inspect `git branch --show-current`, `git status --short` and the exact owned diff.
2. Run `make lint`. There are no `make l` or `make f` targets in the audited Makefile.
3. Format owned Go files with `gofmt -w` and `goimports -w`. For dashboard changes the documented equivalent is `pnpm --dir apps/dashboard format`; restrict Prettier to exact owned files when concurrent edits make a full format unsafe. Format Markdown with `pnpm --dir apps/dashboard exec prettier --write <owned-markdown-paths>`.
4. Inspect formatting, rerun `make lint` and the task's focused tests. Run `make test` and `make build` at each milestone and before enabling a provider. For API changes run `make gen-client`, inspect OpenAPI/AsyncAPI/generated diffs, then `make check-client`, `pnpm --dir apps/dashboard exec tsc --noEmit -p tsconfig.app.json` and `pnpm --dir apps/dashboard exec tsc -b`.
5. Run relevant integration tests with `LOOMPLANE_TEST_DATABASE_URL` pointing at an isolated test database. Do not stop shared development processes. Full integration is `make test-integration` with the documented database running.
6. Stage exact owned paths, inspect `git diff --cached`, commit the coherent change and push `main` after checks pass. If the remote moved, inspect and reconcile without force push or discarding concurrent work. Record a blocked gate or push accurately.

New security, network and parser tests must exercise behavior, failures and scope. Documentation-only changes use formatting, link/path checks and `git diff --check`; they do not need artificial unit tests.

### Task 0: Qualify provider and gateway contracts

Dependencies: none. Outputs are evidence and fixture contracts, not enabled provider support.

**Files:** Create `docs/qualification/miget.md`, `docs/qualification/federation-gateway.md`, `tests/acceptance/federation/README.md`, and sanitized fixtures under `internal/clouds/miget/testdata/`. Inspect `internal/gateway/program/go.mod.txt` and the source for that exact Bastion version.

**Interfaces:** Record provider capability names and result states `supported`, `unsupported`, `unverified`. Task 1 encodes these; absence always means unavailable.

- [ ] Record the exact docs/API revision and verify scoped workspace auth, permitted read checks, pagination, app/deployment identifiers, immutable artifact deployment, disabling auto-deploy, secret delivery, logs and readiness. Record each required endpoint and request/response shape, including denied cases.
- [ ] Prove workload identity or explicitly select gateway identity only. Test that neighboring applications cannot retrieve the credential. Record the provider path that enforces backend isolation.
- [ ] Reproduce replacement DNS behavior, test a supported non-disruptive remedy, and record whether a remote caller can keep using a stable name. An unresolved stale record blocks secure discovery availability.
- [ ] Resolve IPsec allocation order, peer updates, returned public IP, redundant tunnel support, cleanup and address release. Start with an imported connection; do not guess by allocating paid resources.
- [ ] Build a small gateway qualification fixture against the pinned Bastion dependency. Assert verified client SAN identity, HTTP/2/gRPC behavior, stream drain, credential rotation and atomic route replacement. Record unsupported requirements and the selected standard implementation for them before Task 7/8.
- [ ] Checkpoint the documents and fixtures through the verification procedure. Live probes require a named test account, resource inventory, cost envelope and cleanup handles. Missing access leaves the relevant gate unverified while offline work continues.

### Task 1: Add scoped destinations and capability snapshots

Dependencies: Task 0 contract names. Gate: schema and tenant isolation.

**Files:** Create shared contracts, management stores and `database/migrations/federation.go` from the map; tests `internal/federation/service_test.go`, `database/migrations/federation_test.go`, `tests/integration/federation_destinations_test.go`. Modify `internal/security/catalog.go`, `database/migrations/{migrations,connection_uses}.go`, `internal/platform/platform.go` and add `extensions/federation/extension.go`.

**Interfaces:** `Service.CreateDestination(ctx context.Context, p security.Principal, in federation.Destination) (*federation.Destination, error)`; `Service.RefreshCapabilities(ctx context.Context, p security.Principal, destinationID string) (*federation.CapabilitySet, error)`. Server derives scope from authorized resources, not input `OrgID`. `CapabilitySet` carries revision, checked time, expiry, identity profiles, protocols, DNS/network modes, lifecycle operations and reason per unavailable capability.

- [x] Write `TestDestinationScope` to assert a foreign connection/project is not found, duplicate physical cluster aliases remain recognizable, and an imported destination causes no provider mutation. Write `TestCapabilitiesExpireClosed` and interrupted-migration rerun tests.
- [x] Run `go test ./internal/federation ./database/migrations -run 'TestDestinationScope|TestCapabilitiesExpireClosed|TestFederationMigration' -count=1`; verify missing behavior fails.
- [x] Implement scoped stores and Warden permissions. Pin cluster UID/namespace UID or provider account/resource identity and invalidate observations when identity changes. Add connection deletion references, including cleanup and trust issuer uses.
- [x] Implement additive, rerunnable migration statements and explicit feature flags defaulting off. Advertise observed capabilities separately from static provider catalog support; keep legacy plugin capabilities empty by default.
- [x] Rerun unit tests and `go test -tags=integration ./tests/integration -run TestFederationDestinations -count=1` with the isolated database. Verify old schemas upgrade and old APIs still read existing bindings.
- [x] Complete the verification procedure and commit `feat: add scoped federation destinations`.

### Task 2: Define version 2 manifest intent and compatibility

Dependencies: Task 1. Gate: old plans and manifests remain unchanged.

**Files:** Create `pkg/manifest/federation.go`, `federation_test.go`; modify `pkg/manifest/{manifest,schema,edit}.go`, `schema.json`, `internal/manifests/edits.go`, `docs/manifest.md`. Add fixtures to `pkg/manifest/testdata/`.

**Interfaces:** `manifest.Federation` contains typed destination, link, export, grant and placement maps. `ValidateFederation(m *Manifest) Problems` reuses the existing `manifest.Problem` fields `Path` and `Message` and the existing `Problems` error type. Structured planning findings retain their existing codes.

- [ ] Write `TestFederationManifestV2` for valid intent, wrong port/protocol, unknown source/destination, duplicate exports, invalid grant expiry, mixed legacy/v2 links and explicit exposure. Add `TestV1RoundTripUnchanged` and fuzz seeds for oversized selectors and nested objects.
- [ ] Run `go test ./pkg/manifest ./internal/manifests -run 'TestFederationManifest|TestV1RoundTrip' -count=1`; confirm failures before implementation.
- [ ] Implement strict version dispatch, parser/schema parity, environment overrides, editor round-trip and limits. Preserve comments through the existing edit mechanism. Reject unknown fields and mixed link models; document a reviewed v1-to-v2 conversion, with no automatic application.
- [ ] Reconcile the version 2 fields with the [one-click services plan](../../one-click-services-plan.md). Produce one shared schema/version transition for federation, recipe sources, storage and protection, with separate optional sections. Neither workstream may reserve a competing meaning for version 2.
- [ ] Make protocol constraints explicit: HTTP path/method selectors are invalid for opaque TCP; UDP is unsupported. Scope cross-environment references explicitly and reject cross-organization grants while disabled.
- [ ] Run focused tests and `go test ./pkg/manifest -run '^$' -fuzz FuzzParse -fuzztime 30s`; inspect version 1 fixture diffs and complete the verification procedure.
- [ ] Commit `feat: describe federated services and access in manifests`.

### Task 3: Plan reachability, permission and disruption together

Dependencies: Tasks 1-2. Gate: unverified private paths stay blocked.

**Files:** Create `internal/planning/{federation,reachability}.go` and corresponding tests; modify `plan.go`, `planner.go`, `links.go`, `targets.go`, `service.go`, `internal/deployments/credentials.go`, and `database/migrations/connection_uses.go`.

**Interfaces:** `EvaluateReachability(in ReachabilityInput) ReachabilityResult`; new input includes scoped destinations, links, exports, grants and capability revisions. Result contains separate network, DNS, identity, policy and readiness requirements plus typed findings. `PlanFederation(in FederationInput) ([]Action, []Finding)` emits deterministic resource actions and activation barriers.

- [ ] Write `TestReachabilityRequiresEveryLayer`, `TestNeedsDoesNotGrantAccess`, `TestFederationPlanRevision`, and `TestCyclicRuntimeCalls`. Assert a tunnel without a grant remains blocked, gateway identity cannot meet a workload grant, and a token rotation preserving scope does not alter desired state.
- [ ] Run `go test ./internal/planning -run 'TestReachability|TestNeeds|TestFederationPlan|TestCyclicRuntime' -count=1`; verify failures.
- [ ] Add network/policy subjects to `Action` and all exhaustive consumers. Build directed paths using destination IDs, not cloud-name pairs; retain the old GCP/AWS path for v1. Separate deployment prerequisites from runtime call edges.
- [ ] Include costs, regional restrictions, restarts, shared-resource impacts and capability versions in reviewable changes. Block unknown disruption and insufficient identity. Revalidate account/network/namespace identity at execution.
- [ ] Test held sources, same-cloud different accounts, IPv4/IPv6 overlap, stale approval, connection deletion and old `private-across-targets` regressions. Run `go test ./internal/planning ./internal/deployments -count=1` and relevant connection-use integration tests.
- [ ] Complete verification and commit `feat: plan cross-destination reachability and access`.

### Task 4: Persist and watch live service endpoints

Dependencies: Tasks 1-3. Gate: G1 catalog behavior.

**Files:** Create `internal/runtimecatalog/{catalog,pgstore,watch,kubernetes}.go`, tests beside them, and `tests/integration/runtime_catalog_test.go`; extend `database/migrations/federation.go`, `internal/realtime/event.go` and `internal/telemetry/telemetry.go`.

**Interfaces:** `Catalog.Observe(ctx context.Context, agent federation.AgentIdentity, batch federation.ObservationBatch) error`; `Catalog.Snapshot(ctx context.Context, requester federation.AgentIdentity, request federation.SnapshotRequest) (*federation.Snapshot, error)`. Observations are fenced by destination epoch and generation. A snapshot has a revision, continuation cursor, endpoints and tombstones.

- [x] Write `TestEndpointLeaseAndGeneration`, `TestCatalogScope`, `TestSnapshotAcrossReconnect`, and `TestEndpointTombstoneWins`. Use a fake clock: endpoint is unavailable at 30 seconds without renewal; generation 8 cannot overwrite deletion at generation 9.
- [x] Run `go test ./internal/runtimecatalog -run 'TestEndpoint|TestCatalog|TestSnapshot' -count=1`; confirm failing behavior.
- [x] Implement scoped persistence, lease expiry, atomic paginated snapshots and EndpointSlice list/watch with resource-version resync. Observe readiness/serving/terminating correctly; avoid assuming a terminated pod address remains valid.
- [x] Enforce registered address/port bounds, reject metadata and cross-destination endpoint injection, and cap observations per batch. Use bounded per-scope events for updates and a full snapshot after cursor expiry.
- [x] Run `go test -race ./internal/runtimecatalog -count=1` and `go test -tags=integration ./tests/integration -run TestRuntimeCatalog -count=1`. Prove an interrupted snapshot cannot mix revisions or leak a foreign export.
- [x] Complete verification and commit `feat: track live service endpoints by destination`.

### Task 5: Enroll agents and workload identities

Dependencies: Tasks 1 and 4, Task 0 identity qualification. Gate: authenticated observation and issuance.

**Files:** Create `internal/workloadidentity/{issuer,spiffe,enrollment,trust}.go`, `internal/federation/enrollment.go`, relevant tests, `internal/agent/{agent,watch,store}.go`, `cmd/loomplane-agent/main.go`, and `deployments/federation/agent.yaml`. Modify configuration and platform registration.

**Interfaces:** `Enroll(ctx context.Context, request federation.EnrollmentRequest) (*federation.Enrollment, error)`; `VerifyPeer(ctx context.Context, chain []*x509.Certificate, expectedDomain string) (federation.AgentIdentity, error)` behind the selected SPIFFE library adapter. An enrollment contains a one-use, short-lived bootstrap reference, destination identity and allowed attestation method; it never lets a peer choose its own workload selector.

- [ ] Write `TestEnrollmentCannotBeReplayed`, `TestWorkloadCannotChooseIdentity`, `TestForeignTrustBundleCannotImpersonate`, `TestRestoredAgentStartsClosed`, and `TestCredentialRotation`.
- [ ] Run `go test ./internal/workloadidentity ./internal/federation ./internal/agent -run 'TestEnrollment|TestWorkload|TestForeignTrust|TestRestored|TestCredential' -count=1`; verify failure.
- [ ] Integrate SPIRE registration and workload API consumption using version-pinned supported libraries. Pin selectors to cluster/service-account or verified server workload identity; use isolated credentials for qualified managed-runtime profiles. Keep private keys local or in scoped secret delivery.
- [ ] Implement outbound authenticated agent watch, persisted public state and monotonic epoch. Request namespace-scoped observation permissions; document any separately required cluster enrollment operation. Agent credentials cannot access management API routes.
- [ ] Run real local SPIRE rotation/expiry and revoked-bootstrap tests in the acceptance fixture. Assert wrong trust domain, wrong destination, missing attestation and restored state are denied.
- [ ] Complete verification and commit `feat: enroll destination agents with workload identities`.

### Task 6: Compile and distribute default-deny service policy

Dependencies: Tasks 3-5. Gate: bounded authorization and revocation.

**Files:** Create `internal/access/{policy,compiler,snapshot,revocation}.go`, tests, and `internal/agent/apply.go`; extend federation routes/store, realtime events and Warden permissions.

**Interfaces:** `Compile(in federation.PolicyInput) (federation.PolicyBundle, error)`; `Apply(ctx context.Context, bundle federation.PolicyBundle) (federation.Acknowledgment, error)`; `Revoke(ctx context.Context, p security.Principal, grantID string, expectedGeneration uint64) error`. Bundle contains signed scope, epoch/generation, grants, route references, not-before/expiry, and required capability versions.

- [ ] Write `TestDefaultDeny`, `TestGrantExpiryClosesSessions`, `TestPolicyReplayRejected`, `TestOfflineLeaseExpires`, `TestRollbackKeepsRevocation`, and `TestPolicyGenerationAtomic`. At five minutes without renewal, affected connections close and new calls fail.
- [ ] Run `go test ./internal/access ./internal/agent -run 'TestDefault|TestGrant|TestPolicy|TestOffline|TestRollback' -count=1`; confirm failure.
- [ ] Implement exact source/destination matching, signature verification, monotonic epochs and atomic local activation. A failed snapshot retains only the previous unexpired configuration; no partial permissive update is allowed.
- [ ] Add propose/approve/revoke workflow and acknowledgments. Online revocation closes matching streams on application; disconnected agents show pending revocation with the lease deadline. Enforce clock-skew bounds and restart quarantine.
- [ ] Run package race tests and `go test -tags=integration ./tests/integration -run TestFederationAccess -count=1`. Exercise organization switching, concurrent approvals, revoked human permission and endpoint updates that do not widen policy.
- [ ] Complete verification and commit `feat: enforce versioned service communication grants`.

### Task 7: Deliver the two-cluster private HTTP slice

Dependencies: Tasks 4-6 and qualified gateway profile. Gate: G1.

**Files:** Create `internal/mesh/{gateway,identity,routes}.go`, `internal/runtimecatalog/dns.go`, `internal/agent/probe.go`, `internal/networking/{driver,imported}.go`, tests and `tests/acceptance/federation/two-clusters.yaml`. Extend Kubernetes adapter for owned gateway/service configuration without taking over existing installations.

**Interfaces:** `mesh.Gateway.Apply(ctx context.Context, bundle federation.PolicyBundle, snapshot federation.Snapshot) (federation.Acknowledgment, error)`; `Probe(ctx context.Context, request federation.ProbeRequest) (federation.ProbeResult, error)`; `Resolve(ctx context.Context, caller federation.AgentIdentity, name string) ([]federation.Endpoint, error)`.

- [ ] Write `TestGatewayRejectsForgedIdentity`, `TestProxyCannotBeBypassed`, `TestScopedDNS`, `TestPrivateProbeFromCaller`, and `TestActivationWaitsForAllAcknowledgments`.
- [ ] Run `go test ./internal/mesh ./internal/runtimecatalog ./internal/agent ./internal/networking -count=1`; confirm new security tests fail.
- [ ] Implement destination-local DNS, identity-aware gateway routing, canonical HTTP authorization and private probes. Use established TLS libraries and the qualified Bastion adapter. Keep existing public gateway behavior separate and compatible.
- [ ] Apply network/backend restrictions so all paths to the exported service require identity enforcement, including IPv6 and existing public ingress. Never trust an identity header from an unauthenticated source or forward secrets across redirects.
- [ ] Deploy two isolated local Kubernetes networks. Assert allowed checkout succeeds, denied caller fails, guessed direct backend fails, endpoint replacement keeps the logical name, control-plane interruption obeys leases, and cleanup preserves baseline resources.
- [ ] Record G1 evidence, complete verification and commit `feat: connect private services across Kubernetes destinations`.

### Task 8: Add protocol and runtime profiles

Dependencies: Task 7. Gate: G2.

**Files:** Create `internal/runtimecatalog/{server,managed}.go`, `internal/mesh/tcp.go`, `tests/acceptance/federation/protocols.yaml`; update `internal/clouds/{server,kubernetes}`, `internal/deployments/{ecs,cloudrun}.go`, provider capability descriptions and profile tests.

**Interfaces:** `EndpointObserver.Observe(ctx context.Context, destination federation.Destination) (federation.ObservationBatch, error)`; `ProtocolProfile` declares `HTTP`, `GRPC`, `TCP`, `Streaming`, `ClientIdentity` and `BackendIsolation` as verified capabilities. Unknown profile combinations are rejected.

- [ ] Write `TestGRPCIdentityAndDeadline`, `TestTCPRevocation`, `TestServerObserverOwnership`, `TestUnsupportedRuntimeProfile`, and `TestCloudRunRequiresAudienceToken`.
- [ ] Run `go test ./internal/mesh ./internal/runtimecatalog ./internal/deployments -run 'TestGRPC|TestTCP|TestServerObserver|TestUnsupportedRuntime|TestCloudRunRequires' -count=1`; verify failure.
- [ ] Add server discovery and gateway deployment without changing the one-container service limit. Implement HTTP/2/gRPC method authorization, deadlines and stream draining through the qualified proxy. Use its supported L4 facility for TCP or a separately qualified standard proxy adapter; do not implement a new general-purpose proxy stack.
- [ ] Add an optional MCS bridge for clusters with a verified implementation: map scoped exports to ServiceExport/ServiceImport, detect namespace collisions, and prove a remote export cannot cross Loomplane organization scope. Keep the agent catalog path available when MCS is absent.
- [ ] For ECS and Cloud Run, implement only verified private egress, identity and token audience paths. A VPN alone cannot satisfy private Cloud Run authentication. Providers without a feasible agent/identity path retain explicit limitations.
- [ ] Run local HTTP/gRPC/TCP fixtures with identity rotation, long-lived streams, unauthorized peers, partition, MTU-sensitive payloads and connection limits. Recheck existing ECS/Cloud Run regression fixtures.
- [ ] Record G2 evidence, complete verification and commit `feat: support verified federation runtime profiles`.

### Task 9: Connect Miget and implement its API client

Dependencies: Tasks 0-1. Can proceed before the two-cluster gate, with deployment disabled.

**Files:** Create `internal/clouds/miget/client.go`, `apps.go`, `networks.go`, client tests and `internal/connections/miget.go`; modify connection constants/checker registration, `internal/providers/catalog.go` and provider docs.

**Interfaces:** `miget.New(baseURL string, client *http.Client) *Client`; `Client.Inventory(ctx context.Context, credential []byte, workspaceID, projectID string) (*Inventory, error)`; `connections.MigetChecker` implements existing `Checker`. `Inventory` defines workspace/project/resource/region IDs, visible resources, capability observations and check time, with no secrets.

- [ ] Write `TestMigetWorkspacePinned`, `TestMigetScopedTokenWithoutUsersMe`, `TestMigetPaginationAndRateLimit`, `TestMigetNoCredentialRedirect`, and `TestMigetAmbiguousCreate` using Task 0 fixtures.
- [ ] Run `go test ./internal/clouds/miget ./internal/connections -run TestMiget -count=1`; confirm failure.
- [ ] Implement bounded requests, explicit workspace header, timeout/cancellation, redaction, response decoding, pagination and safe retry classification. Fix the production host to the documented HTTPS origin; allow a test endpoint only through server configuration. Never forward credentials across redirects.
- [ ] Distinguish authentication failure, missing permission, restricted/not-found project, capacity failure and transient outage. Treat a limited resource list as limited visibility, not proof of global absence. Keep registry credential/webhook administration as explicit setup if the scoped token cannot perform it.
- [ ] Implement durable operation lookup contracts for later tasks. A POST with an unknown outcome cannot be repeated unless its identity can be recovered safely. Catalog entry remains unavailable for deployment until Task 10 completes.
- [ ] Run client/checker unit and connection integration tests, complete verification and commit `feat: connect scoped Miget workspaces`.

### Task 10: Deploy and observe immutable Miget applications

Dependencies: Tasks 2-3 and 9; Task 0 artifact qualification must pass. Gate: G3.

**Files:** Create `internal/clouds/miget/{deployments,logs,webhooks}.go`, `internal/deployments/miget.go`, tests, `tests/integration/miget_test.go`; modify `internal/deployments/imagebuild.go`, `internal/planning/targets.go`, `extensions/planning/extension.go` and realtime log wiring.

**Interfaces:** `deployments.Miget` implements existing `Executor`; optional `RuntimeObserver.ObserveEndpoints(ctx context.Context, destination federation.Destination) (federation.ObservationBatch, error)` supplements `Observe` without breaking old executors. Define typed `OperationState` for remote application/deployment IDs, immutable artifact, ownership key and completed stages.

- [ ] Write `TestMigetPinnedArtifact`, `TestMigetRetryDoesNotDuplicateApp`, `TestMigetReplacementKeepsOldService`, `TestMigetScopeChangeBlocksApply`, `TestMigetWebhookReplay`, and `TestMigetDeletePreservesImportedResources`.
- [ ] Run `go test ./internal/clouds/miget ./internal/deployments -run TestMiget -count=1`; verify failure.
- [ ] Implement per-application lifecycle using immutable image/source semantics proven in Task 0. Reuse remote builder/registry support and generalize owned-only image validation without weakening old providers. Disable auto-sync and provider Git auto-deploy on Loomplane-owned apps.
- [ ] Persist operation IDs before polling; recover lost responses by verified ownership. Enforce workspace/project/resource placement, capacity and explicit ports. Store secrets through the approved provider mechanism; fail without revealing values.
- [ ] Poll authoritative state and implement private readiness observations. Verify signed events against raw body, five-minute timestamp window and durable event ID deduplication. Polling remains functional without webhook administration rights; a signed event only triggers observation. Redact provider logs and retain cursor/attempt semantics.
- [ ] Run fixture integration, then a live private application deploy/restart/replace/remove in the approved Miget test account. Verify actual running artifact and DNS after replacement. Record G3 or keep deployment capability disabled with the failed gate.
- [ ] Complete verification and commit `feat: deploy approved applications to Miget`.

### Task 11: Add imported and managed network links

Dependencies: Tasks 3, 7 and 9; Task 0 tunnel sequencing qualification. Gate: connectivity with complete ownership records.

**Files:** Create `internal/networking/{service,ipsec,miget}.go`, `internal/deployments/federation.go`, network tests and `tests/integration/federation_links_test.go`; modify Miget network client, AWS network adapter, orchestration registration and cleanup persistence.

**Interfaces:** `networking.Driver` defines `Apply(ctx context.Context, link federation.NetworkLink, checkpoint Checkpoint) (*LinkOutcome, error)`, `Observe(ctx context.Context, link federation.NetworkLink) (*LinkObservation, error)` and `Remove(ctx context.Context, link federation.NetworkLink, checkpoint Checkpoint) error`. Define `Checkpoint` as `func([]byte) error`; outcome stores owned external handles, observation stores route/DNS/tunnel health without secret material.

- [ ] Write `TestImportedLinkNeverMutatesProvider`, `TestMigetRouteBatchReportsRestart`, `TestNetworkOverlapAndReturnRoute`, `TestSingleTunnelCapability`, `TestUnknownCreateRequiresReconciliation`, and `TestLinkRemovalRetainsAddressCleanup`.
- [ ] Run `go test ./internal/networking ./internal/deployments -run 'TestImportedLink|TestMigetRoute|TestNetworkOverlap|TestSingleTunnel|TestUnknownCreate|TestLinkRemoval' -count=1`; verify failure.
- [ ] Implement read-only imported IPsec/overlay checks first. Verify both ends, route selectors, return path, DNS forwarding and MTU. A shared provider account is never a network identifier.
- [ ] Implement managed AWS/Miget IPsec only through the qualified allocation/update sequence. Check availability/cost before mutation, checkpoint each owned component, and fence concurrent jobs. Batch Miget route changes into one reviewed disruption step; reapproval is required if the affected workload set changes.
- [ ] Preserve imported routes/resources. Remove owned connections and release the gateway address only when no other owner references it. Keep unresolved cleanup, credentials and billing status visible after deployment deletion.
- [ ] Run fixture integration and approved live tunnel loss/recovery/removal. Verify no broad egress route or default route was accidentally installed. Complete verification and commit `feat: manage reviewed links between provider networks`.

### Task 12: Integrate Miget with discovery and service policy

Dependencies: Tasks 7-8 and 10-11. Gate: G4.

**Files:** Create `internal/runtimecatalog/miget.go`, `internal/mesh/miget.go`, associated tests, `tests/integration/miget_federation_test.go`, and `tests/acceptance/federation/miget.yaml`.

**Interfaces:** `MigetObserver` implements Task 8's `EndpointObserver`. `MigetProfile` implements the qualified identity/proxy configuration behind Task 7's gateway interface; it cannot label a gateway-only credential as a workload identity.

- [ ] Write `TestMigetDNSAfterRollout`, `TestMigetIdentityProfileEnforced`, `TestMigetDirectBackendDenied`, `TestMigetNoPublicFallback`, and `TestMigetReverseCallAuthorized`.
- [ ] Run `go test ./internal/runtimecatalog ./internal/mesh -run TestMiget -count=1`; confirm failure.
- [ ] Resolve provider endpoints from inside the destination network, publish only verified current endpoints and apply the Task 0 DNS remedy. If that remedy is disruptive, surface it as a reviewed operation; do not poll attach/detach to refresh DNS.
- [ ] Install the supported workload or gateway identity profile with backend isolation. Block service-level grants when only gateway identity is available. Exercise both cross-cloud directions, not just an AWS-to-Miget health check.
- [ ] Verify a private API call, denied caller, guessed database/backend address, expired identity, revoked grant, gateway restart, stale provider DNS and tunnel outage. Application traffic must never rely on the central API being on its request path.
- [ ] Record G4 evidence and any provider limitations, complete verification and commit `feat: federate private Miget services`.

### Task 13: Add multiple placements and staged stateless rollout

Dependencies: Tasks 3 and 7. Gate: compatibility and safe traffic transitions.

**Files:** Create `database/migrations/federation_placements.go`, `internal/planning/placements.go`, `internal/deployments/placements.go`, tests; modify `internal/planning/{plan,pgstore,planner}.go`, `internal/deployments/runner.go`, SDK optional contracts and generated schemas.

**Interfaces:** `PlacementPlan(serviceID string, desired []federation.Placement, observed []planning.Binding) ([]planning.Action, error)`; add `PlacementID` to actions/bindings and an explicit rollout generation. Legacy actions use `default`; do not change their historic action IDs or replay payloads.

- [ ] Write `TestPlacementBackfill`, `TestTwoPlacementsDoNotOverwrite`, `TestHeldSourcePlacement`, `TestWeightedRolloutWaitsForHealth`, and `TestStatefulPlacementRequiresReplicationContract`.
- [ ] Run `go test ./internal/planning ./internal/deployments ./database/migrations -run 'TestPlacement|TestTwoPlacements|TestHeldSourcePlacement|TestWeightedRollout|TestStatefulPlacement' -count=1`; verify failure.
- [ ] Backfill the placement column and new unique key; update every name-indexed runner/store/CLI/dashboard lookup and connection use extractor. Preserve resources and v1 services as `default`. Keep `Replaced` distinct from simultaneously desired placements.
- [ ] Implement explicit destination selection, capacity validation, local replica limits and weighted traffic steps. Use the same approved artifact across placements. Wait for identity, policy and private readiness before any weight becomes nonzero, then drain before cleanup.
- [ ] Verify legacy workflow replay and a rolling schema upgrade. Rollback must not run an old binary against unsupported multi-placement records; the schema guard refuses it until a reviewed downgrade restores one placement.
- [ ] Complete milestone tests/build and verification, then commit `feat: roll out stateless services across destinations`.

### Task 14: Add bounded reconciliation, failover and cleanup recovery

Dependencies: Tasks 11-13. Gate: G5.

**Files:** Create `internal/orchestration/federation.go`, `internal/deployments/cleanup.go`, tests and `tests/integration/federation_recovery_test.go`; modify drift reporting, workflow registration, connection references and telemetry.

**Interfaces:** `Reconcile(ctx context.Context, scope federation.Scope, expectedGeneration uint64) error`; `RetryCleanup(ctx context.Context, p security.Principal, operationID string) error`. Add `ReconcilePolicy` with approved destinations, immutable artifact, maximum replicas, permitted repairs, retry/rate limits and expiry; runtime health alone cannot create this authority.

- [ ] Write `TestConcurrentReconcilersFenced`, `TestPartitionCannotCreateWriter`, `TestFailoverStaysWithinApprovedPlacements`, `TestExpiredRepairAuthority`, `TestCleanupSurvivesBindingDeletion`, and `TestOldWorkflowStillReplays`.
- [ ] Run `go test ./internal/orchestration ./internal/deployments -run 'TestConcurrent|TestPartition|TestFailover|TestExpiredRepair|TestCleanup|TestOldWorkflow' -count=1`; verify failure.
- [ ] Implement durable leases/fencing, bounded repairs and local-first routing to already authorized healthy placements. Keep the existing drift checker read-only. No background provider move, budget expansion or database promotion is implied.
- [ ] Version the durable workflow build; retain old handlers until active `deployment-v2` runs finish. Recover after crashes at every mutation/activation checkpoint and after database backup restore; require fresh epochs before gateways resume traffic.
- [ ] Implement persistent cleanup retries and operator handoff with full external IDs. Prevent deletion of the only credential needed for unresolved cleanup. Never retry ambiguous paid creation as cleanup.
- [ ] Run race, integration and partition fixtures, record G5, complete verification and commit `feat: recover federated deployments within approved bounds`.

### Task 15: Deliver management API, CLI and compact dashboard flows

Dependencies: stable contracts from Task 3; completion requires Tasks 12 and 14.

**Files:** Extend `extensions/federation/routes.go`, `internal/ctl/{commands,mcp}.go`, `pkg/client/client.go`; create `internal/ctl/federation.go`, `apps/dashboard/src/components/federation/{destinations,networks,services,access-grants,connection-diagnostics}.tsx` and tests. Modify `pages/{infrastructure,integrations,project-detail}.tsx`, `components/{cloud-connections,deploy-dialog}.tsx`, `lib/{pages,roles,realtime-state}.ts` where needed, and generated client/spec files through their generator.

**Interfaces:** Scoped REST collections for destinations, links, exports and grants; read endpoints for catalog, effective access, acknowledgments and cleanup. Draft mutations go through plan approval. Emergency revoke is its own audited authorized operation. CLI exposes `loomctl destinations`, `networks`, `services`, `access` and read-only `diagnose` with structured output; command names are proposed additions.

- [ ] Write integration tests for every route's permission and organization scope. Add UI tests for missing permissions, pending revocation, stale endpoint/DNS, expired grant, provider restart impact, limited Miget capabilities and cleanup failure.
- [ ] Run `pnpm --dir apps/dashboard test` and `go test ./internal/ctl ./pkg/client -count=1`; confirm added flows fail before wiring.
- [ ] Implement routes with Forge metadata and generated clients. Keep denied resources out of navigation and enforce all checks server-side. Show the source, destination, trust profile and actual policy version in an access review.
- [ ] Build compact tables and detail drawers within existing pages. Put connection cost/restart alerts above the table, preserve shared ZeroState treatment and separate empty/loading/denied/error states. Do not introduce a feature tour without the shared onboarding infrastructure.
- [ ] Regenerate OpenAPI/AsyncAPI and clients; run `make check-client`, both TypeScript checks, UI tests and Go route tests. Verify that live events clear on organization changes and failed subscriptions recover through snapshots.
- [ ] Exercise real saved flows at 1440px and 390px, keyboard-only operation, refresh persistence, allowed/denied calls, route-change approval, revoke and retry. Capture evidence and complete verification before committing `feat: manage federated services and access in Loomplane`.

### Task 16: Build the release acceptance matrix and operations guide

Dependencies: all required tasks. Gate: G6, with G0-G5 evidence linked.

**Files:** Create `tests/acceptance/federation/run.py`, `test_contracts.py`, fixture app directories, `docs/federation.md`, `docs/federation-operations.md` and `docs/acceptance/federation.md`. Modify `Makefile`, `docs/{architecture,providers,security,acceptance}.md` and `README.md` only for implemented, verified features.

**Interfaces:** New `make acceptance-federation` runs local fixtures; `make acceptance-federation-miget` requires explicit configured account/resource/cost inputs. The harness emits a sanitized JSON report with scenario, artifact/destination/policy versions, result, timestamps and cleanup inventory. No secrets in command output or reports.

- [ ] Write harness tests proving scope-specific cleanup, retained unrelated resources, missing credentials as explicit blocked results, owned-process termination and failure exit codes.
- [ ] Run `python3 -m unittest discover -s tests/acceptance/federation -p 'test_*.py'`; verify the new contracts fail before implementing the harness.
- [ ] Implement the scenarios in the matrix below, including API restart, agent restart and full snapshot recovery. Capture allowed and denied traffic separately; a health endpoint response cannot satisfy both assertions.
- [ ] Add runbooks for expired credentials, revoked grant still pending, DNS staleness, lost one-time secrets, exhausted IP pool, route-induced restart, provider throttling, orphan billing and control-plane restore. Include exact cleanup ownership and escalation information.
- [ ] Run `make lint`, owned-file formatting, `make lint`, `make test`, `make build`, `make check-client`, full integration and local acceptance. Use the approved live accounts for Miget/AWS acceptance and record actual costs/resources. Run security parser fuzzing and a bounded load test of 1,000 endpoints and 100 concurrent streams; record latency/memory, not an invented SLO.
- [ ] Publish a capability-by-capability evidence table. Keep any failed or unavailable provider profile disabled, record remaining prerequisites, and commit/push the verified release slice. A complete local fixture run is not a claim of live Miget readiness.

| Scenario group      | Required assertions                                                                                                                                               |
| ------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Scope and authority | Foreign IDs denied; same names isolated; readonly keys cannot mutate; agent cannot call management APIs; changed permissions invalidate execution.                |
| Network             | Both directions, overlapping CIDRs rejected, return routes/DNS/MTU checked, single tunnel failure surfaced, no unintended internet ingress or default route.      |
| Discovery           | Endpoint churn, scale-up/down, lease expiry, deletion tombstones, provider DNS after rollout, stale cursor/full resync, failed probe and three-probe readmission. |
| Identity and access | Bad SAN, foreign issuer, forged header, expired credential, rotated bundle, replayed snapshot, active-stream revocation and backend bypass denied.                |
| Deployment          | Immutable artifact, held sources, partial rollout, failed replacement, rollback, concurrent workers, lost create response, 429 retry and cancellation.            |
| Partition/restore   | Local operation within lease, fail-closed deadline, no extra writer, bounded approved failover, fresh epoch on restored gateway/control plane.                    |
| Miget specifics     | Scoped token, hidden project 404, capacity/address 422, route restarts, one-time secret recovery, DNS refresh gate, billing/address cleanup.                      |
| Product             | Saved/reloaded plan, approvals, diagnostics, events, organization switch, empty/denied/error distinctions, keyboard and desktop/narrow layouts.                   |

### Task 17: Optional bilateral federation between organizations

Dependencies: G0-G6 complete. This is a separate opt-in extension, not required for same-organization AWS/Miget orchestration.

**Files:** Extend `internal/workloadidentity/trust.go`, `internal/access/policy.go`, federation stores/routes and access UI; add `tests/integration/federation_partners_test.go` and an additive trust relationship migration.

**Interfaces:** `ProposeTrust(ctx context.Context, p security.Principal, peerDomain string, exports []string) (relationshipID string, err error)`; `AcceptTrust(ctx context.Context, p security.Principal, relationshipID string, expectedGeneration uint64) error`. Both organizations approve their own import/export scope; neither can issue identities in the other's domain.

- [ ] Write `TestFederationNeedsBothApprovals`, `TestForeignIssuerCannotClaimLocalDomain`, `TestEitherOwnerCanRevoke`, and `TestPartnerCannotEnumerateCatalog`.
- [ ] Run the focused trust/access tests and confirm missing behavior fails.
- [ ] Implement independent bundle validation, bilateral grants, limited catalog views and separate revocation epochs. Never flatten foreign trust bundles or copy partner identities into local issuer authority.
- [ ] Verify partition, issuer rotation, relationship removal and cleanup without affecting unrelated organizations. Run integration and real two-domain acceptance before enabling the feature.
- [ ] Complete verification and commit `feat: authorize bilateral service federation`.

## Review checkpoints and prerequisites

The defaults in the spec make the plan executable without choosing a new architecture at every task. Review G0 findings before adopting new proxy dependencies, relaxing workload identity, or accepting weaker immutable deployment semantics. Those changes affect what the product can claim and must be recorded in the design.

Live qualification needs a Miget workspace/project/resource, suitable scoped token, preconfigured registry credential if required, an AWS or other explicitly supported remote network, non-overlapping ranges, an owned test domain if used, and an agreed resource/cost envelope. Required privileges and provider limitations must appear in connection diagnostics. Do not request or store credentials in this document.

The current task produces this plan and its source audit only. Implementation checkboxes remain open. The historical acceptance record is context, and the next implementation run must refresh the checkout, dependency pins and provider documentation before starting Task 0.

Related work: [one-click services and data recovery](../../one-click-services-plan.md) owns recipe qualification, persistent volumes, backup/restore and retained-data deletion. This plan consumes its resource identities and protection contracts when available. Initial Miget database connectivity may adopt an existing managed service with scoped credentials; creating or migrating managed databases and Compose stacks must pass that lifecycle plan as well as federation checks. A resource connection does not imply backup or restore support.
