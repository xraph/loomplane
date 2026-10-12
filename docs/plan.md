# LOOMPLANE — MANDATORY FORGE ECOSYSTEM ARCHITECTURE

## 1. PRIMARY ENGINEERING DIRECTIVE

Loomplane MUST be built on the XRAPH Forge ecosystem.

This is a hard architectural requirement, not an optional technology preference.

The foundational libraries are:

- Forge: https://github.com/xraph/forge
- Ctrl Plane: https://github.com/xraph/ctrlplane
- Dispatch: https://github.com/xraph/dispatch

The wider ecosystem is documented at:

- https://forge.xraph.com
- https://www.xraph.com
- https://github.com/xraph

Loomplane must use these libraries as its primary foundation rather than implementing parallel frameworks for application lifecycle, routing, dependency injection, durable execution, or infrastructure instance management.

**The central engineering principle is: Extend Forge. Compose its ecosystem. Build Loomplane-specific intelligence and orchestration on top.**

Do not create an unrelated Go backend and loosely attach Forge packages afterward.

Loomplane must be a Forge-native application.

---

# 2. MANDATORY INITIAL RESEARCH

Before implementing Loomplane, study the actual source code and documentation for the Forge ecosystem.

Examine:

1. Forge's application and extension architecture.
2. Dependency injection and service registration.
3. Application lifecycle and configuration.
4. Routing, middleware, and OpenAPI generation.
5. Forge dashboard and dashboard-contributor interfaces.
6. Ctrl Plane's instance, deployment, network, provider, and reconciliation packages.
7. Ctrl Plane's Forge extension adapter.
8. Dispatch's durable jobs, workflows, workers, artifacts, and storage implementations.
9. Dispatch's Forge integration.
10. Relevant Forge ecosystem extension packages.

Inspect existing examples and tests.

Do not invent package APIs, constructor names, extension methods, or capabilities.

Do not assume a documented interface has a fully implemented backend.

Pin compatible released versions of the libraries. Check Go toolchain requirements, license compatibility, extension compatibility, and breaking changes.

Document the selected versions and any limitations.

If the Loomplane repository already has a dependency lockfile or Go workspace, account for its existing state.

---

# 3. FORGE AS THE APPLICATION FOUNDATION

Use `github.com/xraph/forge` as Loomplane's core application framework.

Forge should provide supported facilities for:

- Application initialization.
- Dependency injection.
- Service registration.
- HTTP API routing.
- Middleware.
- Configuration.
- Logging.
- Metrics.
- Tracing.
- Health checks.
- Lifecycle hooks.
- Graceful shutdown.
- Extension registration.
- API schema generation.
- Dashboard integration.

Do not introduce a second application framework without a compelling technical reason.

In particular, do not automatically add chi, Gin, Fiber, or another HTTP framework when Forge already satisfies the API requirements.

Do not write a separate dependency injection container.

Use Forge's existing lifecycle and dependency management conventions.

Loomplane-specific functionality should be organized as cohesive application modules or Forge extensions.

Candidate modules include:

- RepositoryDiscovery
- ApplicationGraph
- ManifestManager
- InfrastructurePlanner
- ProviderRegistry
- CloudAccountManager
- DeploymentCoordinator
- NetworkPlanner
- EnvironmentManager
- ReconciliationCoordinator

Each module should have well-defined contracts and be independently testable.

Use Forge extension registration and lifecycle interfaces where appropriate.

Do not force pure domain libraries to depend on Forge unnecessarily. Keep parsers, graph algorithms, schema validators, and planning logic testable as standalone packages.

---

# 4. CTRL PLANE AS THE INFRASTRUCTURE LIFECYCLE FOUNDATION

Use `github.com/xraph/ctrlplane`.

Ctrl Plane must be the preferred foundation for supported:

- Application instance lifecycle management.
- Service instance creation and termination.
- Deployment and release management.
- Deployment strategies.
- Health monitoring.
- Provider abstractions.
- Network routing and service endpoints.
- Resource telemetry.
- Infrastructure observation.
- Reconciliation.
- Tenant-aware lifecycle policies.

Integrate Ctrl Plane through its existing Forge extension where compatible.

Do not recreate Ctrl Plane's functionality inside Loomplane without demonstrating a specific missing requirement.

## Critical provider implementation rule

Ctrl Plane's provider abstraction must not be mistaken for complete implementation of every cloud provider.

Inspect which provider adapters are actually functional.

For Loomplane's first milestones, implement real integrations for:

1. Vercel.
2. Google Cloud Run.
3. AWS ECS/Fargate.
4. Render.

Adapt these integrations to Ctrl Plane's provider interfaces where semantically appropriate.

Where an existing Ctrl Plane interface assumes a long-running container instance but a target uses a different deployment model, implement a documented Loomplane-specific capability adapter.

Do not force incompatible providers into misleading abstractions.

Extend Ctrl Plane upstream where generally reusable improvements are appropriate, and keep Loomplane-specific integration logic within Loomplane.

Avoid copying large amounts of Ctrl Plane source into Loomplane.

## State ownership

Ctrl Plane should own the lifecycle state of resources managed through its services.

Loomplane owns application-level desired state, repository metadata, service dependencies, provider assignments, and approved infrastructure plans.

Maintain explicit mappings between Loomplane application services and Ctrl Plane instances/resources.

Avoid duplicate competing state machines.

---

# 5. DISPATCH AS THE DURABLE EXECUTION FOUNDATION

Use `github.com/xraph/dispatch`.

Dispatch must be the default execution engine for background jobs and multi-step workflows.

Do not build another job queue or introduce Temporal as the default.

Use Dispatch for:

- Repository scanning jobs.
- Git webhook processing.
- Source checkout workflows.
- Build orchestration.
- Infrastructure planning tasks.
- Provider API operations.
- Multi-step deployment execution.
- Deployment verification.
- Scheduled reconciliation triggers.
- Retry and recovery operations.
- Resource cleanup.
- Background health diagnostics.
- Periodic maintenance tasks.

Use its existing capabilities for durable workflow execution, checkpointing, retries, backoff, worker coordination, and dead-letter handling wherever they meet the requirement.

## Deployment workflow

The Loomplane deployment workflow should conceptually contain:

1. Validate repository revision.
2. Load and validate application manifest.
3. Resolve service dependencies.
4. Validate cloud connections.
5. Inspect provider capabilities.
6. Generate infrastructure plan.
7. Request required approval.
8. Validate the approved plan revision.
9. Provision dependencies.
10. Build application artifacts.
11. Deploy services through the selected providers.
12. Configure supported endpoints and connections.
13. Verify application health.
14. Record resulting infrastructure state.
15. Complete deployment or enter an explicit failure/recovery state.

Use Dispatch to coordinate the multi-step execution.

Delegate supported infrastructure instance and deployment lifecycle actions to Ctrl Plane.

Do not duplicate Ctrl Plane's deployment state machine inside Dispatch.

Dispatch should track workflow progress, retries, orchestration dependencies, and execution history.

Ctrl Plane should track its supported infrastructure deployment and instance lifecycle state.

Define how errors and status changes propagate between both systems.

Use a single clearly defined reconciliation authority for each resource type. Do not start overlapping background controllers that independently mutate the same resource.

## Worker execution

Use Dispatch's distributed worker capabilities where supported.

Use separate isolated execution environments for untrusted builds.

Never allow ordinary repository build scripts to execute inside the privileged Forge API process.

Do not interpret a durable job worker as a security sandbox; execution isolation is an additional requirement.

---

# 6. ADDITIONAL FORGE ECOSYSTEM MODULES

Prefer existing compatible XRAPH libraries for their intended responsibilities.

Evaluate the following modules before implementing equivalent functionality.

## Grove — Persistence

Repository: https://github.com/xraph/grove

Use Grove as the preferred database abstraction and ORM.

Use PostgreSQL for the primary production database.

Leverage available migration, transaction, query, and model features.

Implement tenant-isolation policies explicitly; do not assume tenant filtering is automatic.

Do not add another full ORM without a documented reason.

## Authsome — Authentication

Repository: https://github.com/xraph/authsome

Use Authsome as the preferred identity and authentication solution.

Support local self-hosted authentication and optional external identity providers as supported by the library.

Integrate authentication into Forge request handling.

Reuse supported Authsome UI components where helpful.

Do not build a competing user authentication system.

## Warden — Authorization

Repository: https://github.com/xraph/warden

Use Warden for authorization policies and permissions.

Apply authorization to:

- Organizations.
- Projects.
- Repositories.
- Cloud provider accounts.
- Environments.
- Deployment approvals.
- Secret access.
- Infrastructure modifications.
- Administrator actions.

Enforce policies in backend services and API entrypoints.

Do not rely solely on frontend route guards.

## Vault — Secrets

Repository: https://github.com/xraph/vault

Use Vault as the preferred secrets and sensitive configuration abstraction.

Support encryption, scoping, rotation, and secure secret references according to verified capabilities.

Provider credentials must not appear in application manifests or logs.

Evaluate cloud-native secret stores where useful.

Never expose privileged cloud credentials to build workers.

## Keysmith — API Credentials

Repository: https://github.com/xraph/keysmith

Use Keysmith for Loomplane API keys and machine credentials.

Support securely generated and revocable credentials for:

- CLI access.
- Automation.
- CI/CD.
- Future AI-agent integration.

Distinguish Loomplane API credentials from external cloud-provider credentials.

## Chronicle — Audit Trails

Repository: https://github.com/xraph/chronicle

Use Chronicle to provide auditability for security-sensitive operations.

Track:

- Cloud connection changes.
- Deployment approvals.
- Infrastructure modifications.
- Secret access events.
- Resource deletions.
- Authorization changes.
- Administrative actions.

Preserve actor, resource, organization, correlation ID, and operation context.

Avoid recording secret values.

## Trove — Artifact Storage

Repository: https://github.com/xraph/trove

Use Trove as the preferred storage abstraction for:

- Build metadata.
- Deployment bundles.
- Logs or exported reports where appropriate.
- Generated artifacts.
- Repository analysis artifacts.

Use Dispatch's artifact capabilities where appropriate and integrate them with compatible storage backends.

Avoid duplicating artifact lifecycle tracking.

## Bastion — API Gateway

Repository: https://github.com/xraph/bastion

Use Bastion for gateway features where applicable.

Investigate its support for routing, reverse proxying, authentication, service discovery, rate limiting, and traffic policies.

Do not build a competing API gateway from scratch.

Bastion may serve as a gateway on supported customer infrastructure; it must not require all application traffic to pass through the central Loomplane control plane.

## FARP — Service and API Schema Discovery

Repository: https://github.com/xraph/farp

Use FARP when integrating service metadata, API schemas, and gateway discovery.

Distinguish application source-code discovery from live service and API discovery.

Do not assume FARP can automatically understand arbitrary Go, Rust, or TypeScript monorepos without additional Loomplane-specific detectors.

## Relay — Webhooks

Repository: https://github.com/xraph/relay

Use Relay for outgoing webhook delivery, retries, and event notification where supported.

## Herald — Notifications

Repository: https://github.com/xraph/herald

Use Herald for deployment notifications, alerts, and user-facing messaging where needed.

## Other Forge modules

Evaluate additional ecosystem libraries only when they support concrete product requirements.

Candidates include:

- Forge dashboard extensions.
- Forge streaming and SSE extensions.
- Forge MCP extension.
- Forge service discovery.
- Forge observability integrations.
- Confy for configuration.
- Vessel for dependency injection where compatible with the selected Forge version.
- Nexus, Cortex, Weave, Shield, and Sentinel for future AI features.
- Ledger for a future managed SaaS offering.

Do not indiscriminately install every extension.

Choose the smallest compatible set that provides the required functionality.

Do not use removed Forge extension packages as if they were still implemented. Check current migration guidance for database, gateway, storage, cron, and AI functionality.

---

# 7. LOOMPLANE-SPECIFIC FUNCTIONALITY

Forge, Ctrl Plane, and Dispatch provide infrastructure primitives, not the complete Loomplane product.

Loomplane must still implement its distinguishing capabilities.

## A. Polyglot repository intelligence

Build a discovery engine for existing Go, Rust, TypeScript, and JavaScript monorepos.

Discover:

- Workspaces.
- Frameworks.
- Build systems.
- Executable services.
- Shared packages.
- Entrypoints.
- Runtime dependencies.
- Possible infrastructure resources.

Use deterministic parsers first.

Attach evidence and confidence to inferred findings.

Never provision weakly inferred resources without confirmation.

## B. Universal application graph

Represent an entire application as a versioned graph of services, resources, dependencies, and deployment targets.

This graph must be independent of specific cloud providers.

## C. Provider-independent infrastructure planning

Turn application requirements and provider choices into a plan of supported actions.

Use existing Ctrl Plane functionality where appropriate.

Implement Loomplane-specific planning for resources not represented by Ctrl Plane.

## D. Multi-provider deployment coordination

Support a single application with services on different cloud providers.

Dispatch coordinates operations.

Ctrl Plane manages supported infrastructure lifecycles.

Provider adapters implement actual cloud operations.

## E. Unified developer experience

Provide one dashboard that hides unnecessary complexity without obscuring important resource changes.

The interface must support advanced infrastructure inspection and control when requested.

## F. Portable application manifest

Create `loomplane.yaml`.

Keep the specification independent of specific Forge implementation details.

Users should not have to use Forge in the applications they deploy.

Forge is the framework used to build Loomplane itself, not a mandatory framework for Loomplane's customers.

This distinction is non-negotiable.

---

# 8. TARGET APPLICATION ARCHITECTURE

Use this conceptual system design:

**Interfaces**
- Loomplane Web Dashboard
- Loomplane CLI
- REST API
- Future MCP integrations

**Application Runtime**
- Forge application
- Forge dependency injection
- Forge routing and middleware
- Authentication and authorization extensions
- Loomplane domain modules

**Application Intelligence**
- Repository analyzers
- Service graph
- Manifest manager
- Infrastructure planner
- Provider capability registry

**Execution**
- Dispatch durable workflows
- Dispatch job workers
- Artifact processing
- Approval and verification steps

**Infrastructure Management**
- Ctrl Plane
- Provider adapters
- Deployment strategies
- Health and lifecycle management
- Reconciliation

**Supporting Services**
- Grove / PostgreSQL
- Vault
- Keysmith
- Chronicle
- Trove
- Relay
- Herald
- Bastion / FARP where needed

**Deployment Destinations**
- Vercel
- Google Cloud
- AWS
- Render
- Future providers

Maintain well-defined boundaries and ownership between each layer.

---

# 9. DASHBOARD ARCHITECTURE

Evaluate the existing Forge dashboard extension and contributor system before implementing a separate management backend.

The priority is a polished, cohesive developer platform.

Use React and TypeScript for the application interface.

Next.js may be used if it materially improves the UI architecture, but do not introduce a redundant backend or duplicate authentication system.

Prefer an architecture where Forge remains the authoritative API and application lifecycle host.

Integrate with Forge's dashboard shell if it supports the required functionality and developer experience.

The dashboard should include:

- Project management.
- Git repository import.
- Repository discovery results.
- Interactive service graph.
- Cloud provider connections.
- Infrastructure planning.
- Deployment execution and history.
- Logs and health monitoring.
- Environment configuration.
- Networking and resource management.

Reuse existing extension-provided components where practical.

Keep the interface consistent and avoid exposing users to separate Ctrl Plane, Dispatch, and Authsome administration applications unless there is a clear advanced-use case.

To the user, this is one product: Loomplane.

---

# 10. REVISED REPOSITORY ARCHITECTURE

Use a Forge-native modular structure.

Suggested layout:

- `cmd/loomplane/` — Main Forge application entrypoint.
- `cmd/loomplane-worker/` — Dispatch-backed worker process.
- `apps/dashboard/` — Main product dashboard.
- `internal/platform/` — Forge application composition.
- `internal/projects/` — Project and organization logic.
- `internal/repositories/` — Git repository integration.
- `internal/discovery/` — Polyglot repository intelligence.
- `internal/graph/` — Application architecture model.
- `internal/manifests/` — Manifest parsing and validation.
- `internal/planning/` — Infrastructure planning.
- `internal/orchestration/` — Dispatch workflow definitions and integration.
- `internal/infrastructure/` — Ctrl Plane integration and state mapping.
- `internal/providers/` — Provider registry and adapters.
- `internal/networking/` — Cross-cloud network planning.
- `internal/environments/` — Environment configuration.
- `internal/security/` — Loomplane-specific security policies.
- `internal/observability/` — Product-specific telemetry and diagnostics.
- `pkg/discovery/` — Public discovery interfaces.
- `pkg/provider/` — Public provider extension contracts.
- `pkg/manifest/` — Public manifest schema.
- `deployments/docker-compose/` — Self-hosting configuration.
- `examples/` — Sample polyglot applications.
- `docs/` — Architecture and developer documentation.

Create additional packages only when justified.

Avoid mirroring the entire Forge, Ctrl Plane, or Dispatch package hierarchy within Loomplane.

Use their real packages as dependencies.

---

# 11. EXTENSION INTEGRATION STANDARDS

All Forge ecosystem integrations must follow these rules.

1. Prefer an official Forge extension adapter when available.
2. Use documented application lifecycle hooks.
3. Register dependencies through the supported DI mechanism.
4. Prefer existing storage backends over writing new ones.
5. Preserve tenant and request scope across framework boundaries.
6. Ensure consistent authorization and error handling.
7. Propagate traces and correlation identifiers.
8. Avoid duplicate migrations and competing ownership of database tables.
9. Use tested version combinations.
10. Write adapter tests at library boundaries.
11. Document missing capabilities rather than pretending they exist.
12. Prefer upstream fixes for reusable problems.
13. Avoid maintaining private forks unless unavoidable.
14. Keep optional extensions removable without breaking unrelated subsystems.

Create a document called `docs/forge-integration.md`.

For each dependency, record:

- Repository and Go module path.
- Pinned version.
- Features used by Loomplane.
- Integration method.
- Limitations.
- Responsible domain module.
- Required configuration.
- Tests verifying integration.

---

# 12. REVISED IMPLEMENTATION MILESTONES

## Milestone 0 — Forge ecosystem evaluation

Before implementing application features:

- Inspect Forge, Ctrl Plane, and Dispatch.
- Read their extension integration examples.
- Confirm compatible Go and dependency versions.
- Inspect Grove, Authsome, Warden, Vault, and the dashboard extension.
- Review relevant provider interfaces.
- Determine supported versus missing functionality.
- Document architecture and integration boundaries.
- Record license and compatibility concerns.

Acceptance criteria:

A verified technical foundation and documented integration plan.

## Milestone 1 — Forge-native Loomplane foundation

Implement:

- Working Forge application.
- PostgreSQL persistence through Grove.
- Authentication through Authsome.
- Authorization through Warden.
- Basic project and organization management.
- Dispatch registration.
- Ctrl Plane integration.
- Working dashboard.
- Docker Compose installation.

Acceptance criteria:

A user can install Loomplane, authenticate, and manage a project through a functioning Forge-backed API and dashboard.

## Milestone 2 — Repository discovery

Implement:

- GitHub integration.
- Secure repository checkout.
- Go workspace detection.
- Rust workspace detection.
- TypeScript/JavaScript monorepo detection.
- Service identification.
- Dependency extraction.
- Application graph generation.

Use Dispatch for asynchronous repository analysis.

Acceptance criteria:

Import an existing polyglot monorepo and correctly display deployable services and supporting evidence.

## Milestone 3 — Planning and provider assignment

Implement:

- Versioned application manifest.
- Provider capability registry.
- Service-level provider selection.
- Infrastructure planning.
- Plan validation.
- Approval workflow.
- Resource ownership mapping.

Acceptance criteria:

Generate a correct reviewable deployment plan without mutating cloud resources.

## Milestone 4 — Real cloud deployments

Implement:

- Real Vercel deployment integration.
- Real Google Cloud Run integration.
- Secure cloud connection management.
- Dispatch deployment workflows.
- Ctrl Plane-backed lifecycle operations where applicable.
- Build artifact management.
- Deployment progress UI.
- Health verification.

Acceptance criteria:

Deploy a Next.js frontend to Vercel and a Go backend to Cloud Run from one monorepo.

Do not substitute mock deployments for real integrations.

## Milestone 5 — Extended infrastructure orchestration

Implement:

- AWS ECS/Fargate support.
- Managed database provisioning.
- Secure service endpoint configuration.
- Environment-scoped secrets.
- Gateway configuration where supported.
- Partial-failure recovery.
- Drift detection.

Acceptance criteria:

A polyglot application deploys across multiple supported providers while Loomplane maintains consistent deployment and infrastructure state.

## Milestone 6 — Open-source operational maturity

Implement:

- Loomplane CLI.
- Provider SDK.
- Comprehensive documentation.
- Security testing.
- Backup and restoration.
- Upgrade support.
- Expanded observability.
- AI-agent integration foundations.

Acceptance criteria:

Loomplane operates as a complete independent self-hosted developer platform.

---

# 13. MANDATORY REUSE POLICY

Before writing a new infrastructure subsystem, perform this evaluation:

**Question 1:** Does Forge already provide this functionality?

If yes, use Forge.

**Question 2:** Does Ctrl Plane already provide a suitable lifecycle or infrastructure abstraction?

If yes, use or extend Ctrl Plane.

**Question 3:** Does Dispatch already provide the required background execution or workflow behavior?

If yes, use Dispatch.

**Question 4:** Does an existing XRAPH extension provide the functionality?

If yes, evaluate its compatibility, implementation completeness, security, and maintenance requirements. Prefer using it when suitable.

**Question 5:** Is the functionality genuinely unique to Loomplane?

If yes, implement it as a focused Loomplane module.

Do not introduce redundant libraries or infrastructure merely because they are popular.

This policy must not result in forcing an unsuitable dependency into the architecture. A verified missing capability or material mismatch is a valid reason to create an additional adapter or subsystem.

---

# 14. FIRST CODING TASK

Begin by inspecting the Loomplane repository and the Forge ecosystem.

Then:

1. Establish a compatible dependency baseline.
2. Document Forge's application and extension lifecycle.
3. Document Ctrl Plane's existing lifecycle and provider capabilities.
4. Document Dispatch's workflow integration.
5. Create an initial dependency graph showing how the libraries interact.
6. Define the application state ownership model.
7. Define authorization and credential trust boundaries.
8. Create the implementation plan.
9. Set up a runnable Forge-native Loomplane application.
10. Register the appropriate extensions.
11. Connect persistent storage.
12. Implement the first authenticated project API and dashboard view.
13. Add automated tests.
14. Verify startup, shutdown, database persistence, and the API-to-dashboard flow.

Do not stop after documentation.

Deliver the first working vertical slice.

If an expected framework feature is missing, verify against source code and documentation, record the gap, and implement only the minimal required extension.

---

# 15. FINAL ARCHITECTURAL REQUIREMENT

Loomplane is a standalone open-source product built upon the Forge ecosystem.

The Forge ecosystem supplies the foundational framework and infrastructure capabilities.

Loomplane contributes:

- Polyglot monorepo intelligence.
- Application architecture discovery.
- Provider-independent infrastructure specifications.
- Multi-cloud planning.
- Cross-provider orchestration.
- Unified cloud resource management.
- A Vercel-simple user experience.

Customers must NOT be required to write applications using Forge.

Loomplane must support arbitrary existing Go, Rust, TypeScript, and JavaScript applications.

Its deployment engine should remain extensible to other ecosystems.

**Build Loomplane on Forge, Ctrl Plane, Dispatch, and the relevant Forge extensions. Do not reinvent their established functionality.**

The result must be a powerful infrastructure orchestration platform that feels effortless to operate.

**Loomplane — Your infrastructure, woven together.**
