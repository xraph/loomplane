# Own infrastructure and live deployments

Date: October 13, 2026

Status: Accepted for execution by the instruction to continue until the full plan is done. Implementation and acceptance evidence are tracked in the companion implementation plan.

## What you should be able to do

Connect an existing Linux server or Kubernetes cluster, pick it for a Forge service in a project's Deploy tab, review the destination and changes, then deploy. You should see the build's actual output when its integration exposes it, followed by rollout progress and the final result. Reloading the page or reconnecting should recover the current state and retained logs.

The dashboard, CLI and API use the same saved manifest and plan. Each service can have a different target. Managed cloud accounts keep their existing environment defaults.

### Decisions from this conversation

- Existing servers run **containers over SSH**, as selected by you.
- Support both existing servers and Kubernetes clusters.
- Collect build logs when the integration supports them.
- Expose both WebSocket and SSE endpoints through Forge routes.
- Use Forge's generated client realtime and streaming support.
- Plan the work thoroughly and run E2E tests against local Kubernetes in Docker.
- Work in the primary `main` checkout, preserve concurrent changes, and commit and push verified changes.

### Proposed choices that need review

The first server target requires Docker to be installed and reachable by its SSH user. Kubernetes deployments use an existing namespace and your chosen ingress configuration. Source builds run on a connected Docker server acting as a builder; Kubernetes pulls the resulting image from a connected OCI registry. You can also deploy an existing image pinned by digest.

This gives both targets a complete build-to-deploy path without running a repository's build in Loomplane's API process. An agent on each host or an in-cluster build service can be added through the provider interface later.

## Approaches considered

| Approach | What you get | Cost |
| --- | --- | --- |
| Built-in SSH/Docker and Kubernetes targets, with Ctrl Plane lifecycle integration | Targets appear in the existing picker, connections stay in Vault, plans and retries share the current model | Requires connection-specific transports, target references and a reusable build pipeline |
| Separate provider plugins | Existing plugin protocol can carry deployments; infrastructure-specific code runs separately | You must install and operate plugins before the standard infrastructure flow works |
| An agent installed on each server or cluster | Outbound connections can reach infrastructure behind a firewall | Adds agent installation, identity, upgrades and another deployment system |

Use the built-in targets. Preserve provider plugins as an extension point. Loomplane must be able to reach the SSH endpoint and Kubernetes API; the first implementation does not establish tunnels through private networks.

## Current code and the changes it needs

The existing executor contract already has Apply, Remove, Observe, source artifacts and durable checkpoints. Bindings keep the connection that deployed a service, including while removing a replaced service. Reuse that contract and the replacement rules.

Connections are currently chosen once per cloud and environment. Add an explicit per-service target reference so two services can choose different servers or clusters. A plan must pin the resolved connection for each action, including the builder and registry used by a source build.

`useRuns` and `useRunFor` poll live plans and deployments. The log view calls `runLog` to reconstruct messages from recorded state; those messages are useful deployment history, but they are not a provider's build output. Add durable log records and make the view distinguish build output from lifecycle events.

The dashboard's `.forge-client.yaml` currently disables streaming and reads OpenAPI files only. Enable streaming for the Loomplane client, save its AsyncAPI specification, and regenerate the clients from Forge route metadata. Keep the authsome client's current REST configuration.

Ctrl Plane has Docker and Kubernetes providers in the sibling source checkout. Before reusing them, verify the exact module version Loomplane pins, transport injection, connection isolation, secret handling and readiness behavior. Its ambient Docker and kubeconfig defaults must never select an organization's target. Add narrow adapter seams where needed; a separate upstream change must pass that repository's checks too.

## Connections and target selection

### Existing server

Add a `server` connection with a name, SSH host and port, SSH user, pinned host public key, Docker socket path and optional advertised host for published HTTP ports. Store the private key and optional key passphrase in Vault. Use the pinned key for every dial, including checks, builds and drift reads.

Checking the connection verifies SSH authentication, Docker access, daemon architecture and usable build support. It reports ready, incomplete or failed through the existing connection API. A missing Docker installation produces instructions and an incomplete connection. Checking does not install Docker, change the firewall or run sudo.

### Kubernetes

Add a `kubernetes` connection with a name, explicit cluster endpoint, context and namespace. Import a self-contained kubeconfig with embedded CA and token or client certificate credentials. Reject executable auth plugins, external file references and disabled certificate validation. Do not read the control plane's default kubeconfig.

Store the kubeconfig in Vault. Show only its endpoint, context, namespace and check result. Check API access and namespace-scoped permissions for the objects the adapter manages. Public ingress and autoscaling permissions are checked when those features are requested. The namespace must already exist; application cleanup never deletes it.

### Registry and builder

Add a `registry` connection for an OCI registry endpoint, repository prefix and encrypted authentication. A checked server connection can also act as a builder. Registry credentials are used by the control plane for access checks and scoped token exchange. Docker receives the resulting registry token for publishing and server pulls. Private Kubernetes images use a selected existing namespace image-pull Secret, pinned in the connection settings; its credentials and lifecycle remain with the cluster owner.

For a server, the default builder is the target itself and an image can stay in its Docker daemon. A different builder requires a registry. Kubernetes source builds require both a builder and a registry. A digest-pinned existing image skips building and requires only the pull access it needs.

Every referenced connection must belong to the current organization, match its role and be ready. Managed account choices retain their current behavior. Removing a connection that still owns a binding or is pinned by an active plan is rejected with the affected services listed.

### Manifest and plan contracts

Add `target` to service settings and environment overrides. It names the connection ID; the dashboard shows its human-readable name. Add `build.builder`, `build.registry` and `build.image` for source builds and digest-pinned images. The existing `build.context` remains available.

Provider-specific endpoint settings cover a server's host port and a Kubernetes ingress hostname, class and existing TLS Secret reference. The picker exposes the necessary settings compactly, with advanced values available in YAML. Explicit host ports must be validated and reserved for their target; a conflict blocks the plan.

Target changes and build connection changes are included in the reviewed plan and its digest. Add resolved connection IDs to each action/configuration rather than relying on one `Plan.Connections[cloud]` entry. Older plans and manifests keep their current fallback. A changed target is a replacement, and removal uses the old binding's credential.

Connections can rotate credentials under the same ID. Approved plans keep the reviewed target identity and settings; execution reads the current credential. A change to an endpoint, namespace, host key or other placement setting requires a new plan, while a credential rotation that fails its check prevents execution.

## Build and Forge service behavior

Store each source tarball at the commit in the approved lockset. Use the analyzed entrypoint and build context. Respect repository Dockerfiles and Compose contexts. For Forge applications without a Dockerfile, generate a Go container build from the detected module/workspace and declared app entrypoint. Reject a context that cannot contain the module's required files with an actionable finding.

Keep application roles from Forge discovery: HTTP services and workers can deploy; CLI and development helpers remain excluded unless explicitly overridden. Tests include nested modules, multiple declared apps, examples excluded by default, and explicitly included examples. An unsupported build must fail before rollout, not run a guessed command.

A build records its operation/attempt, image tag and final digest. Tag names derive from the organization, project, service and pinned source. Capture output as it arrives, checkpoint the completed artifact, and reuse it on retry. If a provider cannot resume an interrupted build, start a clearly identified new attempt using the same inputs and build cache. Keep its log range separate from the interrupted attempt.

Never pass runtime secrets to a Docker build by default. Apply CPU/memory settings, runtime variables and dependency addresses during rollout. Keep secret values out of Ctrl Plane's persisted specs. Kubernetes uses Secret references; the Docker adapter injects values through its connection-specific execution path and stores only references in the lifecycle record. Host and cluster operators retain their normal access to workloads they run.

Use Forge's `/_/health` for Forge HTTP workloads, with a configured override where an app needs one. Workers use process/pod readiness and running state. A deployment succeeds only after its adapter verifies readiness; public endpoints also receive the existing HTTP verification.

## Rollout and networking

### Server

Run one service container on the selected Docker daemon, on an owned network for its project/environment. Enforce the requested CPU and memory limits. The initial server adapter supports one replica; larger or elastic scale settings are a planning error until a routing/scaling implementation exists.

Private services get an address within their owned Docker network. Public HTTP services use an explicitly reviewed host port and advertised host. Confirm port ownership before replacing a container, reuse the old owned port on updates, and reject collisions with unrelated containers. A same-host update may briefly interrupt service; the plan says so.

### Kubernetes

Manage owned Deployments, Services, runtime Secrets, image-pull Secrets and optional Ingress objects in the selected namespace. Use stable labels containing organization, project, environment and service identity. Check labels before adopting, updating or deleting anything with a matching name.

Use rolling updates and verify observed generation, ready replicas and rollout failures. Fixed replica counts are supported. Elastic scale settings require a working metrics API and the adapter's HPA capability; otherwise they block planning. Public service plans require a supported ingress configuration. Private services use cluster DNS.

### Dependencies and replacement

Services on one server network or one cluster namespace can use their private dependency addresses. The planner must reject a private dependency across disconnected targets. You can configure an externally reachable dependency through the existing environment variables; automatic private network links remain limited to the cloud pairs the current adapters support. A provider choice alone does not prove network reachability.

Build and verify a service on its new target before removing its old binding. If verification fails, preserve the old deployment. Retry cleanup using the old connection when removal fails. Application deletion removes only owned objects and managed secrets; pre-existing namespaces, servers, Docker networks owned by others and user-provided TLS Secrets survive.

## Build-log collection

Log collection is an optional capability with three modes: streamed, polled and unavailable. Report the mode and permission state in provider/connection metadata. A provider's progress messages and a link to its console remain available when it has no readable build-log API.

| Integration | Collection path | Resume key |
| --- | --- | --- |
| SSH Docker builder | Docker build output through the SSH transport | Build attempt and persisted local log sequence; completed image checkpoint |
| Vercel | Deployment events with live follow, with a bounded final catch-up | Deployment/build ID, provider event identity and timestamp |
| Cloud Build | Cloud Logging or configured build-log storage | Build ID and provider entry ID/page cursor |
| AWS CodeBuild | CloudWatch build log group/stream | Build ID, log stream and forward page token |
| Render | Logs API subscription plus historical catch-up when build logs are available | Deploy/resource ID and provider log ID/time |
| Provider plugin | Optional advertised operation-log capability | Operation ID and plugin cursor; explicit gap after unrecoverable restart |

Only collect the build/deploy operation's logs. Do not accidentally mix application runtime logs into the build view. Render's build-log availability and filtering must be verified against its API; a runtime-log subscription alone does not satisfy build-log support.

A collector belongs to the durable deployment activity, not to a browser connection. Closing the page must not stop collection. Checkpoint provider cursors, deduplicate replayed pages using stable provider IDs, retain legitimate repeated identical lines, and drain final output after the provider reaches a terminal status. Cap final drain time and show incomplete collection when the provider never makes the remaining output available.

Missing log permissions and a transient log-reader outage are separate from build failure. Show the reason and retry collection where possible while continuing to record the deployment's authoritative state. Keep v1 plugins compatible; plugins without the optional log capability still deploy and show progress.

## Durable events and Forge streaming

### Records and ordering

Add a durable organization-scoped event log and deployment log storage in Postgres. Each event has an organization cursor, event type, entity identity/revision, project/plan/deployment/action references, timestamp and payload. Log records additionally carry provider operation identity, attempt, source stream and provider deduplication key.

Allocate cursors under an organization row lock in the same transaction as the state update and event append. This avoids a replay cursor passing a transaction that has allocated an ID but has not committed yet. Publish only committed changes. Status writes fail if their authoritative event cannot be committed with them.

Use Forge's producer-owned EventLog contract for replay and Conduit for delivery between API replicas. The Postgres event rows are the replay source. Broadcast wake-ups to every interested replica; competing consumers that send an event to only one API process are incorrect here. Missed wake-ups are recovered by reading the durable cursor, with a bounded background catch-up. Duplicate delivery is expected and deduplicated by cursor.

Retain status events for seven days initially. Retain build logs for seven days with a ten MiB limit per action attempt and a sixteen KiB limit per line. Emit a truncation/gap record when a limit is reached, while continuing status updates. Redact known credential/runtime-secret values before persistence, strip terminal control sequences, and render logs as text. Treat arbitrary repository output as untrusted.

### Routes

Register all transports with Forge's router and identity middleware:

- `GET /api/v1/realtime/ws`: WebSocket subscriptions for permitted entity updates and selected deployment logs.
- `GET /api/v1/realtime/events`: SSE delivery of the same event contracts, with `Last-Event-ID` replay.
- `GET /api/v1/deployments/:id/logs`: cursor-paged retained logs for reload, export and recovery.

Declare named event/request schemas through Forge's WebSocket/SSE metadata. Declare entity updates with `forge.WithStreamBinding` and `forge.Emits` so generated codecs and cache bindings match the REST entity identities. Include plans, deployments and step updates, bindings, connection checks, project/analysis status and manifest changes consumed by the live views.

Full entity upserts and evictions share an ordered cursor. The WebSocket subscribe/resume message identifies the requested scopes and last cursor. A bounded snapshot response supplies current permitted state and a high-water cursor; replay and live delivery cover subsequent events without a snapshot/subscription race. Expired or invalid cursors produce an explicit gap and require resynchronization.

Use Forge-compatible message envelopes whose `event` names the binding and whose `data` contains the actual named entity or log record. Keep cursor and routing metadata outside that entity. SSE carries the cursor as its event ID; WS carries it in the envelope. Test the generated decoder with recorded wire frames so an envelope is never normalized as if it were a deployment entity.

Authenticate before upgrade/subscription, enforce project read access for logs and updates, and validate WebSocket origins. Use existing same-origin session cookies in the dashboard. API-key consumers use the supported authenticated transport; never place reusable bearer credentials in URL query parameters. Recheck membership/access during long-lived streams and close them when access is lost.

### Generated client and dashboard

Generate WebSocket and SSE clients, stream metadata and codecs from saved OpenAPI/AsyncAPI specs. Use Forge client-core's SubscriptionManager, StreamBinder, transport connectors and streaming decoder with the existing normalized cache. Keep one shared, reference-counted connection per organization/session.

Prefer WebSocket for shared subscriptions, with SSE fallback. Use the same generated event bindings on either transport. Configure Vite's API proxy for WebSocket upgrades and include the AsyncAPI spec in the normal generation/check workflow. Regeneration must preserve unrelated authsome output.

Remove the two-second live polling loops once the event bindings cover their queries. On reconnect, cursor gaps, tab visibility return or transport failure, resynchronize through REST and retained-log paging. Keep low-frequency REST fallback while both transports are unavailable. Older REST responses must not overwrite newer streamed entity revisions.

Show connection state as live, reconnecting or stale, including the last successful sync time. A disconnected page cannot promise current status. Update the sidebar, overview, project tabs, infrastructure list and deployment detail from the same cache, including approval from another browser and retry after failure.

The log panel supports source/step/attempt/level filters, bounded rendering, pause/follow, copy and download. New output should not pull you away from a line you are reading. Use the shared ZeroState adapter for a provider without build logs, an empty result or denied access, with different messages for each.

## Delivery sequence

Each stage produces a focused commit after its checks pass. All stages are required to finish this request.

1. **Pin the contracts.** Add manifest/schema/placement fields, resolved per-action targets and log capability types. Preserve older plans. Test two services on different servers/clusters, wrong-organization references, held repository behavior, target changes and stale saves. Primary files: `pkg/manifest`, `internal/planning`, `internal/connections`, `pkg/provider`.
2. **Connect infrastructure.** Add SSH, Kubernetes and registry clients/checkers, encrypted connection forms and inventory summaries. Verify the pinned Ctrl Plane adapter seams and isolate clients by connection. Tests cover host key rejection, namespace RBAC, kubeconfig validation, credential rotation and connection-use deletion protection.
3. **Persist events and logs.** Add migrations, transactional event writes, replay/retention queries, log redaction and provider cursor checkpoints. Wire planning, runner, connection checks and analysis changes. Tests cover commit ordering, duplicate events, truncation and concurrent writes. Primary files: the store packages and a small `internal/realtime` package.
4. **Ship Forge transports.** Register WS/SSE/log routes, integrate the durable EventLog and cross-replica delivery, generate AsyncAPI/client output and configure the development proxy. Contract tests cover authorization, replay, gaps, slow consumers, cancellation and identical payload codecs across transports.
5. **Build and deploy on owned targets.** Add the shared remote image build path, Docker executor and Kubernetes executor/adapter. Preserve replacement and drift behavior. Tests cover owned-resource checks, failed rollout, restart/retry, secret injection, private dependencies and cleanup.
6. **Collect integration logs.** Implement streamed/polled build-log adapters for supported existing integrations and optional plugin logs. Test missing permissions, rate limiting, delayed final lines, same-timestamp events, repeated lines and collector restart. A supported integration is complete only when real provider lines are persisted and delivered.
7. **Connect the product views.** Add compact target/builder/registry selection to Deployment targets and Infrastructure. Use the generated realtime cache bindings throughout live status views, then replace the reconstructed build-log display with persisted output. Test reconnect/status labels, project/organization switching, read denial and desktop/narrow layouts.
8. **Run local acceptance and final checks.** Run the full API/dashboard path against real local Docker/SSH and Kubernetes, capture rendered evidence, remove only test-owned resources, run lint/format/tests/build/generated checks, and push verified commits.

Implementation will use TDD for contracts and failure paths. The execution plan written after this design is approved should split these stages into exact file ownership, commands and expected results, and retain the completion criteria below.

## Local E2E plan

Docker, kind and kubectl are installed and Docker is responding. No kind cluster was present when this plan was prepared. Use a uniquely named test cluster, registry, SSH/Docker builder, network, namespace and isolated database, and record their identities before creating resources.

Keep SSH and registry ports bound to loopback. Configure the registry so the builder and kind nodes reach the same image name. Use explicit contexts for every kubectl command. A privileged Docker-in-Docker daemon, if needed by the isolated builder fixture, belongs only to the disposable test setup; the product does not install one on your server.

Build a small real Forge HTTP service and worker fixture with a Dockerfile and visible build markers. A source fixture server may stand in for GitHub in the deterministic harness; record that boundary. Application authentication, persistence, build, registry push, Kubernetes API, rollout and browser stream delivery must be real.

| Scenario | Required result |
| --- | --- |
| Connect SSH server and Kubernetes namespace | Ready metadata persists; secrets never appear in reads |
| Save different targets for two Forge services | Manifest persists choices; reviewed plan names each destination and source commit |
| Build and roll out to kind | Real image is pushed/pulled, pods become ready and the HTTP service answers |
| Deploy to SSH/Docker | Owned container runs on the selected daemon and its reviewed port answers |
| Follow build output | Marker lines arrive through the generated Forge client before the build finishes |
| Force SSE fallback | The same entities/log lines update when WebSocket is unavailable |
| Disconnect/reconnect during a build | Retained lines replay once, current step/status recovers |
| Lose an API replica or restart the deployment worker | A second API serves replay; operation/checkpoint and status converge |
| Build failure and retry | Error output persists, UI shows failure, retry keeps distinct attempt history |
| Failed target replacement | Old service remains; successful retry removes it only after readiness |
| Change status from another browser | Lists, counters and detail update without manual reload |
| Revoke read permission or switch organizations | Old subscriptions close and old organization data is removed |
| Expired cursor or bounded log retention | Gap/truncation is visible and REST recovery restores current status |
| Remove the application | Owned containers/objects/secrets/bindings disappear; unrelated resources survive |

Verify the rendered dashboard at desktop and 390px widths. Test transport connection failures, access denial and retry separately from empty results. Capture the target selections, real build output and final rollout/cleanup results.

The harness cleans up by the recorded ownership names/labels. It must not reset a user's kubeconfig, prune Docker globally, delete an existing namespace, remove an unrelated cluster or stop the running development processes. Keep a failed run's diagnostics until they are captured, then remove its test resources.

## Completion criteria

- Both target kinds connect, persist, appear in the picker and run real Forge services through reviewed plans.
- Every action uses its pinned target and source; replacements, retries, secrets and drift preserve ownership.
- Supported integrations deliver real build output, and unsupported/denied log readers state their limit accurately.
- WS and SSE use Forge routes and generated client contracts, with durable replay and cache recovery.
- All live product views converge after disconnect/restart and show when current status is unavailable.
- Local SSH/Docker and Kubernetes E2E scenarios pass, including failure, retry and cleanup.
- Go/dashboard lint, formatting, unit/integration tests, build and client generation checks pass before each relevant commit/push.

Local acceptance establishes these adapters against the tested Docker/Kubernetes versions. It does not establish every production cluster's ingress, registry, authentication method or network configuration. Report those configuration requirements and any unverified managed-provider log acceptance explicitly.

## Registry authentication ruling, October 14

The final review found that native Docker and kubelet authentication can follow a registry's challenge to a token realm the control plane has not reviewed. Docker operations now receive only a direct registry token or a repository token minted through the approved realm. Basic-only authenticated Docker operations are refused with setup instructions. Anonymous pull and push access are checked separately, including cleanup of an empty push probe.

Generic Kubernetes image-pull Secrets do not consume direct bearer tokens. Select an existing namespace Secret through `image_pull_secret`; its name is covered by each action's reviewed target settings. Preflight requests a credential-free Secret Table row, validates its name/type and refuses full-object fallback. Loomplane preserves that external Secret through rollback and removal. This replaces the proposed creation of native pull Secrets from the control plane's registry password. Runtime application Secrets remain owned and managed by Loomplane.

## Source references

Repository contracts: `pkg/provider/provider.go`, `pkg/provider/sdk/sdk.go`, `internal/deployments/runner.go`, `internal/planning/plan.go`, `internal/planning/planner.go`, `extensions/planning/routes.go`, `apps/dashboard/src/lib/use-runs.ts`, `apps/dashboard/src/lib/runs.ts`, `apps/dashboard/.forge-client.yaml`.

Forge source contracts: `client_meta.go` (`WithStreamBinding`, `Emits`), `streaming.go` (producer-owned replay), `packages/client-core/src/stream.ts` and `live.ts`, and the TypeScript WS/SSE generators. The installed dashboard client-core exposes the cache/realtime primitives; generation is currently disabled for Loomplane streams.

Provider references checked while preparing this plan:

- [Docker daemon access over SSH](https://docs.docker.com/engine/security/protect-access/)
- [Kubernetes kubeconfig organization and trust](https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/)
- [kind local registry configuration](https://kind.sigs.k8s.io/docs/user/local-registry/)
- [Vercel deployment events and live follow](https://docs.vercel.com/docs/rest-api/reference/endpoints/deployments)
- [Cloud Build log storage and permissions](https://docs.cloud.google.com/build/docs/securing-builds/store-manage-build-logs)
- [CloudWatch GetLogEvents pagination](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html)
- [Render log subscription](https://api-docs.render.com/reference/subscribe-logs)
