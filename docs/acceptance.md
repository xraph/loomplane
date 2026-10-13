# The acceptance run

Everything Loomplane deploys is tested against fakes that answer the way each cloud's API does. The acceptance run is the other half: it deploys a real repository to your real accounts, checks every public address answers, and takes it all down again. Run it before you rely on a target, and after a cloud changes its API.

It costs money while it runs. Most of it is the few minutes services and databases exist, but a link's VPN and a database are billed by the hour, and a run that's interrupted can leave things behind. It takes everything down when it ends, pass or fail, unless you ask it not to.

## Running it

### Verify the dashboard flow on Vercel

You can also test the managed deployment controls through a running Loomplane dashboard, using the Vercel account you connect under Integrations. Keep the token in that form. You don't need to export it for this flow.

1. Create a separate test project from a public web app or static site and analyze it. `aurorascharff/astro-demo` at commit `71eae662d03453c3b8333f59de368783828395d0` is a static Astro sample with no backing resources.
2. Save the draft in the Deploy tab, select Vercel for the service, and check the cloud account shown for the environment. With several ready Vercel accounts, select the one you set aside for testing.
3. Create a plan. Check the source commit and manifest revision, confirm that its only creation is the test service, then approve it.
4. Wait for the deployment to succeed. Open the served URL, navigate to another route and exercise a client control. The Astro sample's Islands page has a Svelte counter you can increment.
5. Turn the test service off in Deployment targets and save the placement. Plan again, review its deletion, and approve cleanup. Check that cleanup succeeds, the environment has no service binding, and the old URL no longer serves the app.

The October 13, 2026 browser run completed this sequence with the Astro sample, including the interactive counter and Vercel project removal. This verifies Vercel's static-site path. The other targets still need their own cloud acceptance runs.

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
