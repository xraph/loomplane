# Run Loomplane on Miget

You can run Loomplane on [Miget](https://miget.com) as a Compose Stack built
from `deployments/miget`. Miget builds the image from this repository, so you
don't need a published release. You get the API and dashboard, the analysis
worker and a managed PostgreSQL database.

## Deploy

Generate a secret key first, and keep it in your secret manager:

```bash
openssl rand -base64 32
```

Then create the stack in Miget:

1. Choose **Compose Stack**, pick this repository and the branch you want to
   track, and set the compose path to `deployments/miget`.
2. On the review step Miget asks for `LOOMPLANE_SECRET_KEY` and fills in a
   random value. Replace it with your key. Loomplane only accepts 32 bytes
   encoded as base64, and it won't start with anything else.
3. Pick a Resource with room for the stack. It asks for 3 GB of RAM (1 GB each
   for the API, the worker and the database), a 5 GB artifact volume and 5 GB
   of database storage.
4. Deploy, wait for both builds, and open the `loomplane` app's URL.

Create your account and organization, then connect a repository.

Loomplane derives its encryption keys from that secret, and stored
credentials can't be decrypted without it, so set it once and leave it alone
on later deploys.

## What Miget runs

| Service            | On Miget                    | Notes                                      |
| ------------------ | --------------------------- | ------------------------------------------ |
| `loomplane`        | Public app on port 5000     | API and dashboard, with the artifact volume |
| `loomplane-worker` | Private app                 | Repository analysis                        |
| `db`               | Managed PostgreSQL          | Backed up daily by Miget                   |

`compose.miget.yaml` switches both apps to production and wires them up. The
public URL comes from Miget's `${{loomplane.public_url}}` and the database URL
from `${{db.url}}`, so you never type either one. If you move to a custom
domain, check that both apps see it in `LOOMPLANE_PUBLIC_URL`, because sign-in
redirects are built from that address.

Miget runs every Compose service as its own app and drops `entrypoint:` and
`command:`, which is why the image picks its process from
`LOOMPLANE_PROCESS`. It's `api` unless you set `worker`. Any other value stops
the container with an error, so a typo can't quietly start a second API.

## Optional settings

The stack leaves out the GitHub App, email and recovery storage. Add them as
environment variables on the Miget apps once the stack is up:

- The six `LOOMPLANE_GITHUB_*` credentials go on both apps. Set them together;
  see the [GitHub App guide](github-app.md).
- The `LOOMPLANE_NOTIFICATIONS_*` SMTP settings go on `loomplane`. Production
  uses implicit TLS, usually on port 465.
- `LOOMPLANE_BACKUP_URL` and `LOOMPLANE_BACKUP_BUCKET` go on `loomplane` if you
  want recovery objects off the artifact volume.

The [production Docker guide](docker-production.md) explains each one.

## Try it locally

Plain `docker compose` ignores `compose.miget.yaml`, so you can run the same
stack on your laptop in development mode, with a Postgres container standing
in for the managed database:

```bash
cd deployments/miget
cp .env.example .env
# put the output of `openssl rand -base64 32` in LOOMPLANE_SECRET_KEY
docker compose up --build --wait
```

Open <http://localhost:5000>.

## Things to know

Miget builds the image twice, once for each app, and each build compiles the
dashboard and the Go binaries. Expect the first deploy to take a while.

On the first deploy the worker can start before the API has finished
migrating the database. It restarts until the migrations are in, then
settles.

Miget picks the PostgreSQL version for the managed database. The production
Docker stack pins PostgreSQL 17, so check which major version you got before
you restore a backup from elsewhere.

Miget backs up the database every day and keeps those backups for at least
seven days. It doesn't back up the artifact volume. Point
`LOOMPLANE_BACKUP_URL` at independent object storage if you need recovery
objects to survive losing that volume.
