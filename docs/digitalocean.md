# Deploy to DigitalOcean

Loomplane can run your services on DigitalOcean App Platform and give them Managed PostgreSQL, MySQL and Valkey databases. App Platform builds each service from your repository, so you don't need a registry or a builder of your own.

## Connect DigitalOcean

1. In DigitalOcean, open **API**, then **Tokens**, and create a personal access token with read and write access. A custom-scoped token needs `app`, `database` and `account:read`.
2. Under **Settings**, then **GitHub**, give DigitalOcean access to the repositories you'll deploy. App Platform clones them through that connection; Loomplane never hands it your code.
3. In Loomplane, open **Integrations**, choose **DigitalOcean** and paste the token.

Loomplane asks DigitalOcean whose token it is before it keeps it. A token belongs to one team, and that team is where every app and database is made. A token from a locked account is refused.

## Point a manifest at it

```yaml
version: 1
defaults:
  provider: digitalocean
  region: ams
services:
  web:
    source: apps/web
    needs: [api]
  api:
    source: services/api
    needs: [db]
resources:
  db:
    kind: postgres
    version: "16"
```

The regions are App Platform's: `nyc`, `sfo`, `tor`, `atl`, `ams`, `fra`, `lon`, `blr`, `sgp` and `syd`. Leave `region` out and you get `nyc`.

Each service in each environment becomes an App Platform app of its own, named after the service, the environment and the project, with deploy on push turned off. A service with a Dockerfile builds from it. Anything else goes through App Platform's buildpacks, which handle Node, Go, Python, Ruby, PHP and more; a `start` command in the manifest replaces the one they'd pick. Workers run as App Platform workers and static sites as static sites. `size` picks the smallest instance that fits, from `apps-s-1vcpu-0.5gb` up to `apps-d-8vcpu-32gb`, and `scale.min` sets how many instances run.

Secret variables go in as App Platform secrets, so DigitalOcean encrypts them and hides them in its control panel.

## Which commit runs

Every other target here deploys the exact commit a plan approved. App Platform can't: it always builds the newest commit of a branch, and DigitalOcean has no way to ask for a particular one. So Loomplane checks instead.

It points the app at the branch your project reads, starts the deployment itself, and watches the commit App Platform reports for every component. If it's the approved commit, the deployment goes ahead. If someone pushed to the branch after you approved the plan, Loomplane cancels the deployment before it goes live and the step fails with both commits in the message. In the rare case a wrong build gets live before the check sees it, Loomplane rolls the app back to what ran before, or deletes the app if nothing approved ever ran there.

The practical upshot: don't push to a branch while a plan for it waits for approval. If your repository's default branch isn't `main`, set the branch on the project's repository in Loomplane, because a repository left on its default branch is built from `main`. Get it wrong and the commit check catches it and tells you. To deploy another branch for a while, such as a quiet one nobody pushes to, analyse it with **Another branch**: a plan builds the branch its analysis read.

A brand-new app's `ondigitalocean.app` address takes a few minutes to appear in DNS after App Platform says it's live. Loomplane waits for it in public DNS, for up to ten minutes, before it checks that the service answers, so this machine never caches the name as missing. If it still isn't there, the step says the app is deployed and that retrying it checks again.

## Databases

| Manifest kind | Made on DigitalOcean                            | Versions                   |
| ------------- | ----------------------------------------------- | -------------------------- |
| `postgres`    | Managed PostgreSQL                              | 17, 16, 15                 |
| `mysql`       | Managed MySQL                                   | 8                          |
| `redis`       | Managed Valkey, which speaks the Redis protocol | DigitalOcean's current one |

Each is a single `db-s-1vcpu-1gb` node in the datacenter that matches the region: `nyc` gets `nyc3`, `ams` gets `ams3`, `fra` gets `fra1`, and so on. You can name a datacenter outright, such as `nyc1`, as a resource's region. The services that need a database get its connection string as a secret, with TLS required. Loomplane doesn't set trusted sources, so add them in DigitalOcean if you want only your apps to reach it.

Removing a database from the manifest deletes it with its data, so take a backup first if you need one.

## App Platform limits

A private service is only reachable from inside its own App Platform app, and each service here gets its own app, so nothing can call one. The planner says so and asks you to make it public. App Platform custom domain attachment remains a separate managed-platform capability; server ingress routes below do not configure App Platform domains. Build logs stay on the app's **Activity** tab, and Loomplane shows the deployment's progress.

## Shared Droplet capacity and independent DNS

You can also provision managed Droplets in an organization Cloud, adopt a pinned SSH/Docker host, and place services through server pools. This uses the server runtime. Your existing App Platform manifests keep using the managed runtime.

Start with `loomctl clouds create --organization --name Shared`, then run `loomctl cloud-servers options cloud_... conn_...` for the connected DigitalOcean account's supported regions, sizes and bootstrap images. Review a typed server creation input with `cloud-servers preview`, accept its returned review with `cloud-servers apply`, and follow the persisted operation through provider actions and enrollment. A provider action can complete before Docker is ready. Read [Clouds and deployment targets](clouds.md#manage-servers-and-pools) for the complete commands, pool budgets and adoption rules.

DigitalOcean compute and DNS capabilities are checked independently. Your App Platform token scopes do not establish Droplet or DNS permissions: the selected operation must have the required account permission, and unsupported or denied operations fail before new effects. Keep credentials in Integrations.

For a managed DNS zone, use `loomctl dns-zones create --file zone.json` with `mode: "managed"`, the zone name, your DNS connection ID and allowed project IDs. You can use a different account for compute and DNS. An external zone instead returns an ownership proof and manual record instructions. Neither mode buys a domain or changes registrar nameservers.

Routes use reviewed server ingress and retain exact provider DNS receipts. DNS, TLS and backend status are reported independently. Follow route history and any drain deadline before removing old capacity, and supply the maximum previously served external TTL when you manage DNS yourself. The new record's TTL does not tell you what clients still have cached.

Stop older app and worker binaries before upgrading the database. Pending mixed managed/server attempts without provider quiescence proof and legacy unfenced attempts require reconciliation; they are not repaired by blindly retrying. Fixture-backed provider and ACME checks do not qualify your DigitalOcean account, DNS propagation or public certificate issuance. Verify those with your own authorized live scope before a production move.
