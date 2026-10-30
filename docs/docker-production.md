# Run Loomplane in production with Docker

You need a Linux host with Docker Engine, Docker Compose 2.24.4 or newer,
and a hostname pointed at that host. Open TCP ports 80 and 443 for Caddy's
HTTPS certificates and browser traffic. Start with 2 CPU cores, 8 GB RAM
and enough disk for your repositories, database and recovery objects; measure
your workload before choosing a smaller host.

This stack runs one API and dashboard, one analysis worker, Postgres 17 and
Caddy. It tolerates container restarts. Host failure still takes it offline,
so keep backups off the host. It does not provide high availability.
For that, run the [Kubernetes chart](kubernetes.md).

## Install

Download the Docker bundle from the GitHub release you want to run. Verify
its checksum against the release's `SHA256SUMS`, then extract it into
`/srv/loomplane`. The bundle includes an image digest in `image.txt` and
pins `.env.example` to that digest.

You can also use `deployments/production` from a source checkout. In that
case, choose a published image tag or digest yourself. The example version
is a placeholder, not a promise that a release has been published.

Run these commands from the directory containing `compose.yaml`:

```bash
umask 077
cp .env.example .env
openssl rand -base64 32
openssl rand -hex 32
```

Edit `.env`. Set `LOOMPLANE_IMAGE`, `LOOMPLANE_DOMAIN` (a hostname without
`https://` or a path), and `ACME_EMAIL`. Put the first generated value in
`LOOMPLANE_SECRET_KEY` and the second in `POSTGRES_PASSWORD`.

Keep that secret key in your secret manager. Loomplane derives encryption
keys from it, and your database backup needs the original key to recover
stored credentials. Generate it once, not on each deployment.

The stack passes secrets through container environment variables. Protect
`.env` with mode 600 and restrict Docker access to administrators. Don't
paste `docker compose config` or `docker inspect` output into tickets;
both can reveal those values. Use `config --quiet` to validate safely.

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 240
docker compose ps
```

Open `https://YOUR_HOSTNAME`. Setup needs a one-time token, so get the link
first. The API logs it as a `setup is waiting` warning with a `url` field,
and `loomplane setup-token` prints the same link:

```bash
docker compose exec loomplane loomplane setup-token
# Finish setup at https://YOUR_HOSTNAME/setup?token=...
```

Open that link, create your account and organization, then connect a
repository. Once setup is done the command prints `Setup is complete.`

Caddy obtains and renews the TLS certificate and forwards WebSocket and SSE
traffic to the API. Health and metrics routes are blocked at the public
proxy.

If GHCR requires authentication, run `docker login ghcr.io` with a token
that can read the package. A newly published package may need its visibility
changed to public in GitHub before anonymous pulls work.

## Configuration and storage

The API runs migrations before it becomes healthy. Compose starts the
analysis worker after that check passes. Both run as UID 10001 with a read-only
root filesystem, dropped capabilities, a PID limit and a temporary `/tmp`.
The API gets a writable artifact volume, and the worker gets its own scratch
volume for repository checkouts. Neither container gets the host's
Docker socket; connections to deployment targets use Loomplane's normal
credential and approval flow.

| Volume          | Contents                                                        |
| --------------- | --------------------------------------------------------------- |
| `postgres-data` | Accounts, credentials, jobs, deployment state and audit records |
| `artifact-data` | Deployment source artifacts and local recovery objects          |
| `analysis-work` | The worker's scratch space for repository checkouts             |
| `caddy-data`    | TLS certificates and Caddy's certificate authority state        |
| `caddy-config`  | Caddy's saved runtime configuration                             |

One analysis can unpack up to 2 GiB of source, which is more than the
1 GiB RAM-backed `/tmp`, so checkouts go to `analysis-work` on disk. Keep a
few GiB free on the host for it. The worker runs two analyses at once by
default. Set `LOOMPLANE_ANALYSIS_CONCURRENCY` in `.env` to change that (the
minimum is 1). Each extra analysis needs more disk and more memory, so raise
the worker's `mem_limit` in `compose.yaml` along with it. The volume is only
scratch space, so you don't need to back it up.

Volume names carry the Compose project prefix `loomplane-production`.
Keep that project name stable across upgrades. `docker compose down` keeps
the data; `docker compose down --volumes` deletes it.

Only Caddy publishes host ports. Postgres is on an internal Docker network;
the API and worker also have outbound access for GitHub and cloud APIs.
Database traffic stays on this host's Docker network with TLS disabled.
If you move Postgres off-host, supply a separate Compose override with a
certificate-verified database URL and remove the bundled database dependency.

Set the optional GitHub App variables together when you need private
repositories. See the repository's [GitHub App guide](https://github.com/xraph/loomplane/blob/main/docs/github-app.md).
You can put a multiline PEM in a single-quoted `.env` value. Set the webhook
secret in both Loomplane and GitHub.

Email is disabled until you configure SMTP. Set the SMTP host, credentials
and sender for password recovery and email notifications. Production uses
implicit TLS, usually on port 465. Verify delivery with your mail provider
before relying on account recovery.

Open Loomplane in a browser after the first start. A new instance sends
you to `/setup`, where you enter the setup token (see above) and create the
first account and organization. See
[Running Loomplane](operations.md#first-run) for what setup does.

| Variable                       | Default      | What it does                                                                                           |
| ------------------------------ | ------------ | ------------------------------------------------------------------------------------------------------ |
| `LOOMPLANE_WAITLIST_ENABLED`   | unset        | `true` or `false` forces the waitlist on or off. Unset or empty uses the stored setting.               |
| `LOOMPLANE_CONSOLE_PATH`       | `/__console` | Where platform admins reach the console. One path segment starting with `__`.                          |

Put them in `.env` next to the Compose file and both services pick them up.
An instance that existed before the waitlist stays open after you upgrade
unless you set `LOOMPLANE_WAITLIST_ENABLED=true`. On an upgraded instance,
run this once to give someone console access:

```bash
docker compose exec loomplane loomplane admin grant-platform-admin you@example.com
```

[Running Loomplane](operations.md#the-console) covers both.

`LOOMPLANE_BACKUP_URL` can point recovery objects at an independent storage
backend. Without it, they share the local artifact volume. This setting
does not schedule backups of Loomplane's own database.

## Check health

```bash
docker compose ps
docker compose logs --tail=100 loomplane loomplane-worker
docker compose exec -T loomplane wget -qO- http://127.0.0.1:8780/_/health
docker compose exec -T loomplane-worker wget -qO- http://127.0.0.1:8781/_/health
```

The probes include extension health, so a stalled durable worker can make
a container unhealthy even while its HTTP listener still answers. The API
replaces a durable worker that a database timeout stopped, and turns
healthy again once the replacement is polling. Docker's
restart policy restarts exited processes; it does not restart a running
unhealthy container. Alert on unhealthy state, investigate the cause, and
restart the affected service after database or provider access recovers.

Logs rotate at 10 MB per file with five files per container. Send logs and
metrics to your monitoring system before you need longer retention. Keep
metrics access inside a trusted Docker network.

## Back up and upgrade

The database backup contains encrypted credentials. Store it off-host and
keep the encryption key separately. Back up the artifact volume too: it can
contain recovery objects that cannot be recreated by fetching source code.

Before an upgrade, stop both writers, take a backup using the currently
installed image, and copy it off the host:

```bash
umask 077
mkdir -p backups
docker compose stop loomplane-worker loomplane
docker compose run --rm --no-deps \
  -v "$PWD/backups:/backups" --user "$(id -u):$(id -g)" \
  loomplane backup --out /backups/pre-upgrade.tar.gz
```

Back up `artifact-data` while the API is stopped, using your volume backup
tool. Record the current image digest, and retain the old Compose files and
`.env`. Replace the deployment files with the next release bundle, update
only `LOOMPLANE_IMAGE` in your existing `.env`, then:

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 240
docker compose exec -T loomplane loomplane migrations
```

Check sign-in, a repository analysis and a deployment's live status after
the upgrade. Take a fresh backup before retrying an upgrade that partly ran.

Changing the image back is safe only when the database schema is compatible.
Loomplane refuses a schema migrated by a newer version. To recover from an
incompatible upgrade, stop both application services, use the previous image
with a new empty database volume, and restore your pre-upgrade backup:

```bash
# Point compose.yaml at the recovery database volume before running this.
docker compose up -d --wait postgres
docker compose run --rm --no-deps \
  -v "$PWD/backups:/backups:ro" --user "$(id -u):$(id -g)" \
  loomplane restore /backups/pre-upgrade.tar.gz
docker compose up -d --wait --wait-timeout 240
```

Restore the matching artifact data and original secret key as part of recovery.
Keep the failed database volume until you've verified the restore. Test this
procedure on a separate host before your first upgrade. More operational
details are in [Running Loomplane](https://github.com/xraph/loomplane/blob/main/docs/operations.md).

## Build and publish a release

From the repository root, you can build and test your own image:

```bash
make image VERSION=v0.1.0
bash scripts/test-production.sh loomplane:v0.1.0
```

The test creates a unique Compose project with random credentials, a local
TLS certificate and a random loopback port. It checks the dashboard,
authentication, blocked public health routes, extension probes, database
backup and persistence across container recreation. It removes only that
project's containers and volumes. Public certificate issuance, mail delivery,
GitHub App installation and real cloud deployment need separate live checks.

To publish, push a `vMAJOR.MINOR.PATCH` tag that's on `main`, or run the
Release workflow from the Actions tab with the version you want. A manual run
tags the commit you started it from, so start it from `main`:

```bash
gh workflow run release.yml --ref main -f version=v1.2.3
```

Prerelease suffixes such as `-rc.1` are accepted. The release workflow runs
lint, unit and integration tests and creates the GitHub release, then builds
and smoke-tests native AMD64 and ARM64 images. Both must pass before the multi-platform version tag is
published to `ghcr.io/xraph/loomplane` with provenance and SBOM attestations.
This follows Docker's [multi-platform GitHub Actions workflow](https://docs.docker.com/build/ci/github-actions/multi-platform/).

The workflow attaches a Docker deployment bundle, checksum, manifest details
and immutable image reference to the GitHub release. A release of `v1.2.3`
publishes `v1.2.3` and `1.2.3`, and moves `1.2`, `1` and `latest` only while
it's the newest release in each of those lines. Rerunning or patching an
older release can't drag `latest` backwards, and a prerelease moves nothing.
A `0.x` release skips the major tag. You can check what a tag would publish
with `bash scripts/release-image-tags.sh v1.2.3`. With the Docker Hub secrets
below, every one of those tags is mirrored there too. Pin the exact version
or the digest from `image.txt` in production; the moving tags are for trying
things out. Wait for the workflow to finish before announcing that an image
is available. Your repository must have Actions
enabled, Linux ARM64 runner access and permission to write GHCR packages.

The same release gets binaries from GoReleaser. `loomctl`, the CLI, ships
for macOS, Linux and Windows, and installs with `brew install xraph/tap/loomctl`
or from the `xraph` Scoop bucket. The `loomplane_*` archives carry the API,
the worker, `config.yaml` and the built dashboard for Linux and macOS. Unpack
one, set `LOOMPLANE_DASHBOARD_DIR=./dashboard` and run `./loomplane` from that
directory. Homebrew and Scoop update only when the repository has a
`HOMEBREW_TAP_GITHUB_TOKEN` secret that can push to `xraph/homebrew-tap` and
`xraph/scoop-bucket`. Without it, the archives still publish.

When one part of a release fails, you don't need a new version. Run the
Release workflow again with the same version and set `target` to `cli` or
`docker`. Only that part publishes, from the existing tag, into the existing
release. Tick `skip-tests` if that tag already passed them:

```bash
gh workflow run release.yml -f version=v1.2.3 -f target=docker -f skip-tests=true
```

You don't need a release to run the latest code. Once CI passes on `main`,
the Docker workflow publishes AMD64 and ARM64 images as
`ghcr.io/xraph/loomplane:main`, which moves with `main`, and
`ghcr.io/xraph/loomplane:sha-<commit>`, which never moves. Pin the `sha-`
tag in `LOOMPLANE_IMAGE` so a restart doesn't pull a newer build you haven't
read. Those images skip the release checks and the ARM64 smoke test, so keep
them for staging and early trials. Add `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` secrets to mirror both tags to Docker Hub as well.
