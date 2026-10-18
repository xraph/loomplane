# Workload identity qualification

Status: identity primitives, persisted enrollment, outbound runtime transport, and joined PostgreSQL/SPIRE acceptance are implemented. Task 5 remains open. You cannot enroll a production destination or start an authenticated Loomplane agent from this checkpoint alone.

## Run the local fixture

You need Docker and the repository's Go toolchain:

```sh
python3 tests/acceptance/federation/identity/run.py
```

To exercise persisted enrollment and endpoint discovery through live SPIRE credentials, add PostgreSQL:

```sh
python3 tests/acceptance/federation/identity/run.py --postgres
```

That mode creates an internal Docker network and a pinned PostgreSQL 17.11 container with a temporary memory-backed data directory. It exposes no host ports. The runner removes its own database container and network after the fixture exits.

The runner builds for your Docker host's architecture, creates a temporary image from the pinned SPIRE images, and uses `--network none` for the default SPIRE-only mode. It exposes no host ports. The build context contains only the fixture binary and Dockerfile, and cleanup selects only that run's random container and image names.

## Pinned dependencies

| Component          | Pin                                                                                            |
| ------------------ | ---------------------------------------------------------------------------------------------- |
| go-spiffe          | v2.9.0                                                                                         |
| spire-api-sdk      | v1.15.3                                                                                        |
| SPIRE server       | 1.15.4, index digest `sha256:a728418a76e5054e977680a7c70a291f93389294e4bef00e2aaad65324f39357` |
| SPIRE agent        | 1.15.4, index digest `sha256:adc9e89e171af5764b29f9ffc9d3296b8033ba8c6ca11bcadbb56099170d526e` |
| Local public state | Grove SQLite driver v1.7.3                                                                     |

The SDK has no v1.15.4 tag. Its v1.15.3 registration and revocation calls passed against the pinned 1.15.4 runtime. SPIRE documents [API compatibility across releases](https://github.com/spiffe/spire-api-sdk).

## Verified behavior

The live fixture passed on Linux arm64 in a disposable container:

- The Unix attestor issued the requested identity only when UID, executable path and SHA256 matched the registration. A deliberately wrong executable digest did not provide that SVID.
- The same consumed join token could not attest a second fresh SPIRE agent. The test requires an explicit token rejection in the agent log; a connection timeout alone cannot pass it.
- The Workload API rotated a 30-second X509-SVID. Loomplane rejected the expired certificate while accepting the rotated certificate.
- Wrong trust-domain and destination expectations rejected the live credential. Disabling the test registry entry rejected an otherwise valid certificate.
- The issuer created and deleted entries through the real SPIRE API.

Unit and race tests cover foreign roots, removal of old roots during rotation, malformed trust replacement, caller-selected identity fields, exact control-plane audience and restored public state. Agent state uses transactional sequence reservation. Every process starts closed, the same epoch cannot reactivate restored state, and a newer epoch prevents an older process from reserving more observations.

The default in-memory registry isolates certificate verification from persistence. The optional PostgreSQL mode joins those paths; neither mode establishes fleet-wide revocation during a partition.

`make build` passed. The exact proposed Go tree passed `go test ./...`, and the identity packages passed the race detector. Full shared-checkout tests and lint encountered concurrent Kubernetes storage changes while that work was incomplete; those files are preserved. Identity package lint is checked separately. This checkpoint does not close the full-repository lint gate.

## Attestation constraints

Kubernetes registration pins namespace, service account, pod UID and container name, with the namespace UID checked against the destination record before registration. These selector names follow the [SPIRE Kubernetes attestor](https://github.com/spiffe/spire/blob/v1.15.4/doc/plugin_agent_workloadattestor_k8s.md). The Kubernetes profile still needs live qualification and a controller that produces the verified binding.

Unix registration requires a verified parent node, UID, executable path and SHA256. The [Unix attestor](https://github.com/spiffe/spire/blob/v1.15.4/doc/plugin_agent_workloadattestor_unix.md) must enable workload-path discovery. Keys remain in the local Workload API source. The public agent state database stores no private keys or reusable bootstrap credentials.

## Persisted enrollment

You can exercise the enrollment store against PostgreSQL with `TestEnrollmentCannotBeReplayed`. It allocates epochs on the server, checks the destination generation and physical identity, and activates an observer in the same transaction that consumes its five-minute reference. Eight concurrent consumers produce one activation. A failed observer insert rolls back both retirement of the old observer and consumption of the new reference.

The active registry reads current observer and destination state. Replacement, quarantine and local revocation deny an otherwise valid identity. Failed SPIRE deletion keeps the destination's trust reference for retry. Successful deletion removes the retired observer and enrollment together; the separate epoch counter survives cleanup. Cleanup candidates exclude an active observer whose bootstrap reference has expired.

The management controller requires destination-management and trust-management permissions, a signed-in user, fresh connection identity, and qualified workload attestation. Its request accepts only a destination ID. A trusted resolver supplies workload bindings and export bounds. Runtime credentials cannot become management principals through this controller.

Enrollment race tests, migration reruns, focused package lint, dashboard lint/typecheck, the full build, and all Go tests against the exact proposed commit tree passed. Full shared-checkout lint currently reports formatting in a concurrent Kubernetes PostgreSQL test; this checkpoint preserves that file.

The PostgreSQL integration tests use a fake issuer to inject failures. The joined live fixture now uses the actual enrollment store, runtime catalog, agent transport and SPIRE issuer together. It verifies:

- A fresh store instance reads the persisted enrollment and safely repeats issuance.
- The authenticated enrollment handle can be consumed once.
- An observation is persisted and returned as a complete scoped snapshot.
- A second local state-store process starts closed and cannot reuse the old epoch.
- A fresh enrollment advances the persisted epoch, activates the replacement and fences both the older SQLite process and its still-valid SVID.
- Confirmed SPIRE revocation removes local authority and releases the retained trust reference.

The runner pins PostgreSQL to `postgres:17.11-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24`. This test does not establish ambiguous remote create/delete timeout behavior or private application routing.

## Runtime transport

The separate runtime handler implements enrollment consumption, observation batches and complete snapshot streams. It requires TLS 1.3, exact organization identity, and a current enrollment check on each request. Snapshot sends recheck certificates, trust bundles and active registration. Resumed TLS sessions are rejected.

Read and write deadlines bound slow peers. Mutation contexts end no later than the certificate chain expires. Each agent gets one watch, the handler admits at most 128 concurrent requests, and streams close within 30 seconds so reconnects can pick up rotated credentials. Requests are limited to 2 MiB and snapshots to 16 MiB. The handler has no management routes.

A real local TLS test reuses one connection and confirms that registry revocation denies its next request. Other tests cover actual certificate expiry during a stream, body size and deadline enforcement, bootstrap replay, strict JSON decoding, control-plane organization checks, and request deadlines capped by credential expiry. The local TLS tests use generated certificates. The SPIRE fixture now also exercises the actual agent transport and runtime handler with live Workload API sources on both sides. Enrollment, replay denial, observation, a complete snapshot and revocation passed over mutual TLS. The optional PostgreSQL mode also exercises the persisted enrollment and endpoint catalog behind that handler.

Runtime and identity race tests passed, along with the full build, dashboard lint/typecheck, and all Go tests against the proposed commit and its pinned module files. The shared full-repository lint gate remains open because of concurrent recovery and connection edits.

Catalog snapshots remain unsigned inputs to Task 6. This listener is not registered in production configuration, and its output must not authorize routing.

## Outbound agent lifecycle

The agent transport accepts an HTTPS control endpoint and the exact control-plane SPIFFE audience for its organization. It disables ambient proxies, redirects and HTTP connection reuse. Every request reads the current workload credential and ends by its expiry. Every snapshot frame rechecks the control certificate against current trust before delivery.

You get one enrollment attempt per process. A lost response requires a fresh enrollment because the old handle may already be consumed. Once enrollment succeeds, the coordinator activates durable sequence reservation and starts observation and snapshot reception together. Watch reconnects use bounded backoff without reenrollment. Cancellation or a terminal failure stops both loops and closes local authority. The snapshot receiver must verify signatures and apply policy atomically; transport validation does not replace that step.

Protocol paths, body limits and stream lifetime now live in one shared contract used by both ends. Tests reject malformed frames, truncated JSON, oversized snapshots, foreign scope, changed identity and expired control certificates.

The final live SPIRE rerun, package race tests, scoped lint and proposed-tree Go tests passed. Shared dashboard checks are blocked by concurrent placement code referencing an ungenerated `Endpoint` type; full-repository lint reports concurrent planning and identity changes. Those files were preserved.

## Remaining Task 5 work

Wire the runtime listener and outbound agent executable, connect configuration and namespace-scoped Kubernetes permissions, and implement the verified binding resolver and cleanup scheduler. Qualify Kubernetes workload attestation and ambiguous SPIRE create/delete timeouts, then verify the production launcher and management flows.

The managed-runtime identity profile remains unqualified. Miget activation still requires the test project, credential location, spending limit and live qualification evidence from the [Miget runbook](miget.md).
