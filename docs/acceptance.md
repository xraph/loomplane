# The acceptance run

Everything Loomplane deploys is tested against fakes that answer the way each cloud's API does. The acceptance run is the other half: it deploys a real repository to your real accounts, checks every public address answers, and takes it all down again. Run it before you rely on a target, and after a cloud changes its API.

It costs money while it runs. Most of it is the few minutes services and databases exist, but a link's VPN and a database are billed by the hour, and a run that's interrupted can leave things behind. It takes everything down when it ends, pass or fail, unless you ask it not to.

## Running it

### Verify SSH/Docker and local Kubernetes

With Docker, kind, kubectl, Python 3, Go and the local Postgres running:

```bash
make acceptance-infrastructure
```

The harness records a unique cluster, namespace, Docker-in-Docker builder, registry and network before creating them. It uses an explicit kubeconfig, binds fixture ports to loopback and creates an isolated account, organization and database. Your existing Docker containers and kubeconfig stay in place.

The Forge HTTP and worker applications are real. The harness drives connection checks, manifests, plans, approvals and retries through the application API, builds on Docker, pushes to the OCI registry and waits for real Kubernetes pods. It verifies WS/SSE output before completion, cursor replay on a second API after the first worker stops, private dependencies, failed replacement, build retry history, read revocation, organization isolation, expired cursors, truncation and product-driven removal.

There are fixture boundaries: a source server replaces GitHub, an authenticated SSH bridge forwards channels to the isolated Docker daemon and runs exec requests (the deployment's python3 effect-fence helper) in its builder, and a small HTTP build dependency lets the test cause and recover an actual Docker build failure. Cursor floors and log budgets are advanced in the isolated database to test expiry and limits without waiting seven days or storing megabytes of filler. No Docker build, registry push, Kubernetes rollout or readiness response is fabricated.

Cleanup uses the recorded names and labels and checks that the containers present before the run still exist. It never prunes Docker globally or edits your kubeconfig. A failed run keeps its ownership record and diagnostics under `.superpowers/sdd/2026-10-13-own-infrastructure-live-deployments/`; `state.json` identifies exactly what its cleanup script may remove.

For a rendered browser run, set `LOOMPLANE_ACCEPTANCE_BROWSER=1`. The harness serves an isolated dashboard at `http://127.0.0.1:5190` and writes `browser.json` beside its ownership record. Use the disposable account `acceptance@example.test`, password `Acceptance-Build-logs-42`. At each checkpoint, inspect the view and create the named `continue-<stage>` file. A `disable-ws` file in that directory makes only this test proxy reject WebSockets, so you can verify the generated client's SSE fallback. The dashboard process stops with the run. Using `127.0.0.1` keeps its session separate from your localhost dashboard.

### Verify a composed catalog kind on SSH/Docker and kind

```bash
make acceptance-catalog
```

This uses the same harness, ownership record and cleanup as `make acceptance-infrastructure`, with a different test. You get a composed kind that exists only in this test binary: it's registered from a file built with the `integration` and `acceptance` tags, so the product's catalog never lists it. Its image is a small Go command built for amd64 and arm64, pushed to the run's own registry, and pinned by the digests the pushes report.

The test runs on the kind cluster first, then on the SSH/Docker server. On each, it adds the kind to a project, plans, approves and applies it. The kind provisions a Postgres, runs a `migrate` job that records one row, and starts an `api` that refuses to start without that row and answers `/readyz` only after a short warm-up. The test checks the declared readiness probe on the runtime object and reads the api's control port through the admin proxy and through `CatalogAccess.InternalChannel`. Then it removes the resource. Every binding has to go, with the containers on the server and the workloads, Services, Secrets and ConfigMaps on the cluster, and the Postgres data volume has to stay, as database retention says. On kind, the test makes a retained storage class first, since the default one deletes its volumes.

Both legs pass. If other containers on your machine come and go while it runs, the last cleanup step can still fail: it checks that every container that existed before the run is still there, and someone else's may have gone. By then the run's own cluster, builder and network are already removed, so read the test result above the traceback.

### Verify Octopus on SSH/Docker and kind

This one runs against a disposable Docker server and a disposable kind cluster that you set up yourself. It deploys Octopus 0.5 and two services to each, then checks what the fakes can't: FARP discovery finds a real service, a route you add in Loomplane overrides its FARP route through Octopus's private port, deleting that route brings the FARP route back, the reconcile puts your route back after Octopus restarts, and the admin opens through the sandboxed admin proxy. Then it removes everything through the product.

```bash
export LOOMPLANE_ACCEPTANCE_SERVER=$HOME/acceptance/server.json
export LOOMPLANE_ACCEPTANCE_KUBECONFIG=$HOME/acceptance/kind.kubeconfig
export LOOMPLANE_ACCEPTANCE_FARP_IMAGE=registry.example.test/farp-sample@sha256:...
LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' \
  go test -tags='integration acceptance' -count=1 -timeout=90m -v \
  -run '^TestAcceptanceOctopus$' ./tests/integration
```

Each target skips on its own and names what it still needs, so you can run just the server or just the cluster. With nothing set, both skip and no database or app starts.

| Variable | What it is |
|---|---|
| `LOOMPLANE_ACCEPTANCE_SERVER` | A JSON file for the Docker server, as the server connection takes it: `{"credential": ..., "settings": {"host", "user", "port", "host_key", "docker_socket"}}`. The credential is the SSH private key, or the `{private_key, passphrase}` JSON. |
| `LOOMPLANE_ACCEPTANCE_KUBECONFIG` | A kubeconfig for the kind cluster, with embedded credentials that can create `serviceaccounts`, `roles` and `rolebindings` and use `services/proxy` in the namespace. `_KUBE_CONTEXT` and `_KUBE_NAMESPACE` pick them, or the kubeconfig's own are used. |
| `LOOMPLANE_ACCEPTANCE_FARP_IMAGE` | The FARP sample service, pinned by digest. Both targets need it. It serves an OpenAPI 3 document at `/openapi.json` listing at least one GET path without parameters, answers that path with a 2xx, and isn't Caddy, which the run tells apart from the manual route's target. |
| `LOOMPLANE_ACCEPTANCE_FARP_PORT` | The port the sample service listens on. 8080 by default. |
| `LOOMPLANE_ACCEPTANCE_ZONE` | A Cloudflare zone. Optional. The cluster then gets a hostname in it with a Let's Encrypt staging certificate, and the run checks HTTPS reaches Octopus and `/admin` there is blocked. That also needs `LOOMPLANE_CF_SANDBOX_TOKEN`, `LOOMPLANE_CF_ACCOUNT_ID` and `LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS`, as [the domains run](domains.md#the-acceptance-run) does, plus cert-manager and an ingress controller in the cluster. |

The server gets no domain check. A server serves hostnames only through a Cloud, and this run deploys to a bare connection.

### Verify the dashboard flow on Vercel

You can also test the managed deployment controls through a running Loomplane dashboard, using the Vercel account you connect under Integrations. Keep the token in that form. You don't need to export it for this flow.

1. Create a separate test project from a public web app or static site and analyze it. `aurorascharff/astro-demo` at commit `71eae662d03453c3b8333f59de368783828395d0` is a static Astro sample with no backing resources.
2. Open **New deployment**, select the test project and environment, then choose Vercel for the service. For a first deployment, use the suggested configuration from repository analysis. Save any changed placements and check the account under Accounts. With several ready Vercel accounts, select the one you set aside for testing.
3. Select **Review deployment**. Check the source commit and manifest revision, confirm that the plan's only creation is the test service, then approve it. Unsaved placements and YAML keep Review disabled; hiding the manifest preserves its draft.
4. Wait for the deployment to succeed. Open the served URL, navigate to another route and exercise a client control. The Astro sample's Islands page has a Svelte counter you can increment.
5. Open New deployment for the same project and environment, turn the test service off and save the placement. Select Review deployment, review its deletion, and approve cleanup. Check that cleanup succeeds, the environment has no service binding, and the old URL no longer serves the app.

The October 18, 2026 run verified the full setup page, saved placements, plan review, approval, Vercel build logs, HTTPS route navigation, a client counter and cleanup. The source was the same pinned public Astro sample. The create plan used manifest revision 9; the removal plan used revision 10. After cleanup, the Vercel account showed zero service and resource bindings and the URL returned `DEPLOYMENT_NOT_FOUND`.

The October 14, 2026 browser run completed this sequence with the Astro sample and retained 93 real Vercel build lines across reload. The interactive counter worked, removal succeeded and the old URL returned `DEPLOYMENT_NOT_FOUND`. This verifies Vercel's static-site path and build-log reader. GCP and AWS log adapters have API fixture coverage; their live cloud acceptance remains account-specific.

### Switch a Cloud between Vercel accounts

You can run the persisted Cloud flow against two Vercel accounts or teams:

```bash
export LOOMPLANE_ACCEPTANCE_CLOUD_REPO=your-org/disposable-web-app
export LOOMPLANE_ACCEPTANCE_CLOUD_SOURCE=apps/web
export LOOMPLANE_ACCEPTANCE_CLOUD_VERCEL_A_TOKEN=...
export LOOMPLANE_ACCEPTANCE_CLOUD_VERCEL_B_TOKEN=...
# Set CLOUD_VERCEL_A_TEAM and CLOUD_VERCEL_B_TEAM with the same prefix for team scopes.
LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' \
  go test -tags='integration acceptance' -count=1 -timeout=120m -v \
  -run '^TestAcceptanceCloudAccountSwitch$' ./tests/integration
```

Use a public repository with one web app or static site and no backing resources. Set the source to its analyzed source identifier or directory, using `.` for an app at the repository root. Both tokens must resolve to different verified account or team identities. Missing configuration skips the test, so a skipped run does not qualify either account.

The run creates an isolated database and project, deploys on A, migrates the applied commit to B, checks the destination account and ready deployment URL, then makes another release on B. It confirms that A's project was removed. External routing is acknowledged only after the destination URL answers successfully; custom domains still need their own verification.

Cleanup checks that the generated project name was absent from both accounts before any deployment, then removes only that name in those two scopes, including a project created before a failed binding write. Cleanup errors fail the run. This test always removes its disposable projects and does not use `LOOMPLANE_ACCEPTANCE_KEEP`.

### Run the acceptance harness

You'll need the compose Postgres (`make up`), a public GitHub repository Loomplane can analyze, and a `loomplane.yaml` for it. Then set the accounts you want to test and run it:

```bash
export LOOMPLANE_ACCEPTANCE_REPO=acme/shop
export LOOMPLANE_ACCEPTANCE_MANIFEST=$PWD/acceptance.yaml
export LOOMPLANE_ACCEPTANCE_GCP_KEY_FILE=$HOME/keys/acceptance-sa.json
export LOOMPLANE_ACCEPTANCE_VERCEL_TOKEN=...
make acceptance
```

Only the accounts you set are connected, so the manifest should only name targets on those. A plan that can't be carried out (a missing connection, a permission the key lacks) fails the run before anything is made.

| Variable | What it is |
|---|---|
| `LOOMPLANE_ACCEPTANCE_REPO` | The GitHub repository, as `owner/name`. Public, since the run has no GitHub App. Required. |
| `LOOMPLANE_ACCEPTANCE_MANIFEST` | The `loomplane.yaml` to deploy, as a path. Required. |
| `LOOMPLANE_ACCEPTANCE_GCP_KEY_FILE` | A Google Cloud service account key file. |
| `LOOMPLANE_ACCEPTANCE_VERCEL_TOKEN`, `_VERCEL_TEAM` | A Vercel token, and the team ID if it's a team's. |
| `LOOMPLANE_ACCEPTANCE_AWS_ACCESS_KEY_ID`, `_AWS_SECRET_ACCESS_KEY`, `_AWS_REGION` | An AWS access key and the region to deploy in. |
| `LOOMPLANE_ACCEPTANCE_AWS_CERTIFICATE_ARN` | An ACM certificate, to test HTTPS on ECS. Optional. |
| `LOOMPLANE_ACCEPTANCE_RENDER_API_KEY`, `_RENDER_OWNER_ID` | A Render API key, and the workspace ID if it reaches several. |
| `LOOMPLANE_ACCEPTANCE_TIMEOUT` | How long a deployment may take. 60m by default. |
| `LOOMPLANE_ACCEPTANCE_KEEP` | `1` leaves what was deployed running, so you can look at it. Take it down yourself after. |

## What it checks

1. Each connection you set is checked with its cloud and comes back ready.
2. The repository is analyzed and the manifest saved.
3. The plan can be carried out, and each action is logged.
4. The deployment succeeds, and every step is logged with its address.
5. Every public address answers within five minutes with anything but a server error.
6. A plan for an empty manifest takes it all down, and nothing is left bound.

Use a project and accounts set aside for testing. The run makes services and databases named for an environment called `production` in a project called Acceptance, which is easy to tell apart from what you run for real, but it doesn't look before it makes things.

## A manifest to start from

This covers every target in one run. Trim it to the accounts you have:

```yaml
version: 1
services:
  web:
    source: apps/web
    provider: vercel
    needs: [api]
  api:
    source: services/api
    provider: cloud-run
    needs: [db]
  worker:
    source: services/worker
    provider: ecs-fargate
    needs: [cache]
  docs:
    source: apps/docs
    provider: render
resources:
  db:
    kind: postgres
    provider: cloud-sql
  cache:
    kind: redis
    provider: elasticache
```

For a link, put a Cloud Run service and a private ECS service or an RDS database in the same manifest with `links: {vpn: {between: [gcp, aws]}}`, and expect the run to take longer: an HA VPN takes several minutes to come up.

## Notifications and account recovery

The October 18 run delivered invitation and password-reset email through a real Mailpit container and redeemed the reset link.

To repeat the Mailpit acceptance test, start a local Mailpit instance as described in [operations.md](operations.md#email-and-deployment-notifications), then run:

```sh
LOOMPLANE_ACCEPTANCE_MAILPIT_URL=http://127.0.0.1:8025 \
LOOMPLANE_ACCEPTANCE_SMTP_HOST=127.0.0.1 \
LOOMPLANE_ACCEPTANCE_SMTP_PORT=1025 \
LOOMPLANE_TEST_DATABASE_URL=postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable \
go test -tags=integration,acceptance -count=1 -timeout=90s \
  -run '^TestAcceptanceMailpitInvitationsAndRecovery$' ./tests/integration
```

The test creates an isolated database and synthetic accounts. It selects mail by its unique recipient rather than clearing a shared inbox. Keep Mailpit on loopback. The ordinary integration suite uses its own SMTP receiver, so hosted CI does not depend on Mailpit or production mail credentials.

Render static-site live qualification also passed on October 18: approved create, HTTP 200, approved removal and provider inventory confirmation. Its other workload and datastore paths remain fixture-tested. Google Cloud and AWS acceptance still require their own account runs.
