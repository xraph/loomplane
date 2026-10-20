# Security

Loomplane holds credentials to your clouds and deploys code to them, so it's worth knowing exactly what it protects, how, and what it leaves to you. To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## Who can do what

Everything belongs to an organization, and every request acts in one. A request's organization comes from the signed-in session, or from the API key, and nothing in the request can change it; a dashboard tab that drifted to another organization gets a 409 rather than acting in the wrong one. Another organization's projects, plans, deployments, connections, variables and keys aren't found, the same answer as something that doesn't exist, so an ID never confirms anything.

Roles are Warden roles, checked on every action:

| Role | Can |
|---|---|
| Viewer | Read everything except secret values; change nothing |
| Developer | Create projects and environments, analyze, edit manifests and variables, plan |
| Admin | Approve plans, retry deployments, manage cloud connections, secrets and members |
| Owner | Everything, including the organization itself |

Approving is the line that matters: a developer can plan anything, and only an admin's approval, of the exact revision they reviewed, changes a cloud. An approval of a plan whose world changed since comes back stale.

An API key acts as the member who made it, with their role, in their organization, and never more. A read-only key fails every permission that isn't a read. Keys can't manage keys. When a member leaves or loses a role, their keys lose the same rights on the next request. Every key expires within a year. Keysmith stores a key's hash; the key itself is shown once.

## Where secrets live

| What | Where | Who can read it back |
|---|---|---|
| Cloud credentials | Vault, encrypted with a key derived from `secret_key` | Deployments, in memory, to call the cloud |
| Secret variables | Vault, likewise | Deployments, to hand to the cloud's secret store |
| Database passwords Loomplane generates | Vault, under a key of their own | Deployments, to build the address services get |
| API keys | Keysmith, as hashes | Nobody |
| Passwords | authsome, as bcrypt hashes | Nobody |
| SMTP credentials | Herald, encrypted with a purpose-specific key derived from `secret_key` | Delivery engine only |
| Webhook signing secrets | Relay store wrapper, encrypted with a separate derived key | Signer; one-time response on create or rotation |

Read APIs never return stored credential values. New API keys and webhook signing secrets appear once when you create or rotate them; the audit trail records keys, names and the last four characters of an API key, never a value. Secrets reach a service through the cloud's own secret store: Secret Manager on Cloud Run, Secrets Manager on ECS, sensitive variables on Vercel, readable by that service's runtime identity only. They never pass through Ctrl Plane, which stores what it's given as plain JSON.

All of it depends on `secret_key`. Keep it out of the repository and away from your backups. Losing it loses every credential and secret; leaking it, with a copy of the database, exposes them.

## What runs where

Nothing from a repository runs on a Loomplane machine. Analysis only reads files, in pure Go parsers behind size and file-count limits. Builds run on the clouds' own builders (Cloud Build, CodeBuild, Vercel), in your accounts. Gateways and provider plugins run on your infrastructure, and application traffic never passes through Loomplane.

Provider plugins are trusted with the credentials of the cloud they deploy to, so only configure ones you run or trust. Each needs its own token; a plugin refuses any request without it.

## How it's tested

- Every route's authorization: `tests/integration/security_test.go` asks for each of an organization's things from another organization, as a developer, as a viewer and with a member's key, and fails on anything that isn't refused.
- Fuzzing, for what reads input anyone can send: the manifest parser, repository analysis, the credential parsers, and the code that repacks a repository's tarball, which mustn't write a file outside its directory. `make fuzz` runs them for 30 seconds each.
- Known vulnerabilities: `make vuln`, and CI on every push, runs govulncheck against Loomplane's code and everything it imports. The Go toolchain is pinned in `go.mod` to a release with every standard library fix.
- Static analysis: golangci-lint with gosec, on every push.
- Secrets in tests: integration tests check secrets reach the cloud's secret store and never a task definition, a list response or the CLI's output.

## Recovery and outgoing notifications

Authsome creates, expires and consumes recovery tokens and revokes existing sessions after a password reset. Loomplane fences redemption with a PostgreSQL advisory lock, keyed by a hash of the app and token, because the pinned Authsome implementation otherwise allows simultaneous redemption. Each API admits one reset operation at a time and uses a dedicated bounded database connection, so waiting reset requests cannot consume the application pool. Requests that conflict return 429. Route-specific rate-limit keys keep recovery requests from consuming the sign-in budget while retaining Authsome's shared PostgreSQL limiter.

Recovery links use configured `public_url`. Browser pages set `no-referrer`; reset tokens never enter notification history or audit metadata. Herald's store wrapper removes security message content before the database write, including recipient, subject, body, provider message ID and metadata. Provider error details become a fixed delivery failure message. Invitation creation returns its manual link once and reports email delivery separately.

Deployment payloads contain allowlisted IDs, status, attempt and application link. They exclude build logs, variables and raw provider errors. Delayed mail and inbox reads check current project access and organization membership. Organization owners and admins manage webhook endpoints; possessing an endpoint or inbox ID does not establish access. The outbound HTTP transport blocks private/reserved DNS results and dials only validated addresses, with TLS verification and redirects disabled.
