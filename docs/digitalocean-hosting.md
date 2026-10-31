# Host Loomplane on DigitalOcean

This page is about hosting Loomplane itself on DigitalOcean. If you want Loomplane to deploy your own services to DigitalOcean, that's a different job, and [DigitalOcean as a cloud](digitalocean.md) covers it.

There are three ways to do it. Pick one from the table, then jump to its section.

| | App Platform | Droplet with cloud-init | Marketplace 1-Click |
|---|---|---|---|
| Cost per month | About $42 ($37 for the app, $5 for Spaces) | About $48 for 8 GB, $24 for the 4 GB minimum | The Droplet size you choose |
| Analysis disk | Capped at 2 to 4 GiB | Whatever the Droplet has | Whatever the Droplet has |
| Who looks after the server | DigitalOcean | You | You |
| Upgrades | Redeploy the app | Re-run the install script with a new version | Same as a Droplet |

App Platform is the least work. A Droplet gives you the most room, and the worker isn't squeezed by a small scratch disk. The 1-Click is a Droplet with the install already done.

## App Platform

You need two Spaces buckets before you deploy, one for artifacts and one for backups. Loomplane checks that they exist and doesn't create them. Bucket names are unique per region across all DigitalOcean customers, so use a prefix of your own, like `acme-loomplane-artifacts` and `acme-loomplane-backups`. Create a Spaces access key too.

Then build the storage URL. Keep `us-east-1` exactly as written, because it's the signing region. Put your real region (`nyc3`, `ams3`, `sfo3` and so on) in the endpoint only:

```text
s3://ACCESS_KEY:SECRET_KEY@us-east-1/BUCKET?endpoint=https://REGION.digitaloceanspaces.com&path_style=true
```

`BUCKET` should be your artifact bucket.

Now deploy:

[![Deploy to DigitalOcean](https://www.deploytodo.com/do-btn-blue.svg)](https://cloud.digitalocean.com/apps/new?repo=https://github.com/xraph/loomplane/tree/main)

DigitalOcean asks for four values:

- `LOOMPLANE_SECRET_KEY`: the output of `openssl rand -base64 32`. Keep it in your secret manager.
- `LOOMPLANE_STORAGE_URL`: the Spaces URL above.
- `LOOMPLANE_ARTIFACT_BUCKET`: the artifact bucket name.
- `LOOMPLANE_BACKUP_BUCKET`: the backup bucket name.

DigitalOcean's button officially supports one service, and this template has two (the API and the worker). If the button refuses it, use `doctl` instead. Fetch the template and pull the app spec out of it:

```bash
curl -fsSLO https://raw.githubusercontent.com/xraph/loomplane/main/.do/deploy.template.yaml
sed -n '/^spec:/,$p' deploy.template.yaml | sed '1d; s/^  //' > app.yaml
```

Add a `value:` line to each of the four entries in `app.yaml` that don't have one, then create the app:

```bash
doctl apps create --spec app.yaml
```

When the first deploy finishes, open the `loomplane` component's Runtime Logs and find the `setup is waiting` line. Its `url` field opens `/setup` with the one-time token filled in. You can also open the component's Console and run `loomplane setup-token` to get the link again. Then create your account and organization.

The template uses a DigitalOcean Managed PostgreSQL cluster (billed separately), because App Platform's dev databases don't work: Loomplane needs to create a schema, and the dev database role isn't allowed to. App Platform can't create a production database from a template, so create the cluster first, named `loomplane-db`, or set `cluster_name` under the `db` database to the name of a cluster you already have.

App Platform can't follow an image tag, so the template uses `:latest`. To upgrade, redeploy the app. To stay on one version, change the tag to a release such as `v1.2.3`. The template redirects `/_/metrics` and `/debug` to `/`, so neither is reachable from outside.

Scratch space on App Platform is small, so the worker runs one analysis at a time. Very large repositories can fail there. If yours do, move to a Droplet.

## Droplet with cloud-init

Copy the raw contents of [`cloud-init.yaml`](https://github.com/xraph/loomplane/blob/main/deployments/digitalocean/cloud-init.yaml) from GitHub. The three values sit in the `install.env` block near the top, and they're the only thing you edit:

- `LOOMPLANE_DOMAIN`: the hostname you'll serve Loomplane on.
- `ACME_EMAIL`: where Let's Encrypt can reach you.
- `LOOMPLANE_VERSION`: optional. Leave it empty for the newest stable release.

Create an Ubuntu 24.04 Droplet and paste the whole file into the **User data** box under Advanced options. We recommend 8 GB (`s-4vcpu-8gb`, about $48 a month). You can go down to 4 GB (`s-2vcpu-4gb`, about $24), but then set `LOOMPLANE_ANALYSIS_CONCURRENCY=1` (the bundle defaults to 2). The easy way is to uncomment that line in the cloud-init file before you create the Droplet. On a running Droplet, change the value in `/srv/loomplane/.env` and apply it with `cd /srv/loomplane && docker compose up -d`.

Point an A record for your domain at the Droplet's IP. Caddy fetches the certificate once DNS resolves.

The install takes several minutes. Follow it with `tail -f /var/log/cloud-init-output.log`. When it's done, the setup link is printed at the end of that log, and it shows in the SSH login message until setup is complete. To get it again:

```bash
cd /srv/loomplane && docker compose exec loomplane loomplane setup-token
```

The install generates the secret key and the Postgres password on the box and writes them to `/srv/loomplane/.env` (mode 600). Copy that file somewhere safe now. Without the key, Loomplane can't decrypt the credentials it has stored.

To upgrade, prepare the new version and then start it:

```bash
LOOMPLANE_VERSION=vX.Y.Z /usr/local/lib/loomplane/install.sh prepare
/usr/local/lib/loomplane/install.sh start
```

Preparing the new version also points your `.env` at the matching image and leaves your secrets and domain alone. The stack itself is the one [Production on Docker](docker-production.md) describes.

## Marketplace 1-Click

Once the Loomplane listing is published in the DigitalOcean Marketplace, create a Droplet from it, point your DNS at the Droplet and SSH in as root. The `loomplane-setup` wizard asks for your domain and ACME email, starts the stack and prints the setup link.

If you log in before the first boot finishes, the login message says Loomplane is still installing. Log in again in a minute or two. Secrets are generated on each Droplet at first boot, and none are baked into the image.

Upgrades and the `.env` file work as they do for a plain Droplet.

## Backups

On App Platform the database is a Managed PostgreSQL cluster, and DigitalOcean takes backups daily. Artifacts and recovery objects live in Spaces.

On a Droplet or a 1-Click, follow [Backing up Loomplane](operations.md) and keep a copy of `.env` somewhere off the box. DigitalOcean's own Droplet backups are worth turning on as well.
