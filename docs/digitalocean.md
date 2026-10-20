# Deploy to DigitalOcean App Platform

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

The practical upshot: don't push to a branch while a plan for it waits for approval. If your repository's default branch isn't `main`, set the branch on the project's repository in Loomplane, because a repository left on its default branch is built from `main`. Get it wrong and the commit check catches it and tells you.

## Databases

| Manifest kind | Made on DigitalOcean | Versions |
| --- | --- | --- |
| `postgres` | Managed PostgreSQL | 17, 16, 15 |
| `mysql` | Managed MySQL | 8 |
| `redis` | Managed Valkey, which speaks the Redis protocol | DigitalOcean's current one |

Each is a single `db-s-1vcpu-1gb` node in the datacenter that matches the region: `nyc` gets `nyc3`, `ams` gets `ams3`, `fra` gets `fra1`, and so on. You can name a datacenter outright, such as `nyc1`, as a resource's region. The services that need a database get its connection string as a secret, with TLS required. Loomplane doesn't set trusted sources, so add them in DigitalOcean if you want only your apps to reach it.

Removing a database from the manifest deletes it with its data, so take a backup first if you need one.

## What it can't do yet

A private service is only reachable from inside its own App Platform app, and each service here gets its own app, so nothing can call one. The planner says so and asks you to make it public. Loomplane doesn't attach custom domains; add them to the app in DigitalOcean. Build logs stay on the app's **Activity** tab, and Loomplane shows the deployment's progress.
