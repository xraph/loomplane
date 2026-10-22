# Deploy to Azure Container Apps

Loomplane can build your services and run them on Azure Container Apps, and give them Postgres, MySQL and Redis. It uploads the approved commit to a container registry in your subscription, ACR Tasks builds the service there, and a container app runs the image. Nothing from your repository runs on a Loomplane machine.

## Connect Azure

You connect a service principal: an application registered in Microsoft Entra ID, with a client secret.

1. In the Azure portal, open **Microsoft Entra ID**, then **App registrations**, and register an application. Note its **Application (client) ID** and **Directory (tenant) ID**.
2. Under **Certificates & secrets**, add a client secret and copy its value. Azure only shows it once.
3. On the subscription you'll deploy to, or on the resource group if you'd rather keep Loomplane inside one, open **Access control (IAM)** and give the application the **Contributor** role.
4. In Loomplane, open **Integrations**, choose **Microsoft Azure**, paste the secret, and fill in the tenant ID, client ID and subscription ID. The resource group is optional and defaults to `loomplane`.

If you prefer the CLI, `az ad sp create-for-rbac --role Contributor --scopes /subscriptions/<id>` does the first three steps at once: its `appId` is the client ID, `password` the secret and `tenant` the tenant ID.

Loomplane signs in as the principal before it keeps the secret. A wrong or expired secret is refused with Entra ID's own AADSTS code, so you can tell a mistyped secret from an expired one. Then it asks Azure what the principal's role assignments allow in the resource group, or in the subscription while the group isn't there yet, and lists every action a deployment needs that they leave out. A connection that lacks any is incomplete until you grant them and check it again. Contributor has them all.

Contributor can't assign roles, though, and that decides how your apps pull their images. Give the principal Owner on the resource group, or Role Based Access Control Administrator there as well as Contributor, and each app pulls from the registry as a managed identity called `loomplane-pull`, which holds the AcrPull role on that registry and nothing else. With Contributor alone, apps pull with the registry's admin user, and its password goes into each app as a secret. The connection shows which one you get under `registry_pull`: `identity` or `admin`. Every deployment asks Azure again, so a role you add later counts from the next one. Check the connection again to update what it shows.

Azure takes a while to let a new identity pull. The first deployment after you grant the role can sit at "Waiting for the pull identity's role to reach the container registry" for a few minutes, and Loomplane waits up to ten. If that runs out, retry the deployment.

The subscription also needs the `Microsoft.App` and `Microsoft.ContainerRegistry` resource providers. Loomplane registers them on the first deployment if you haven't, which Contributor on the subscription may do. With Contributor on just a resource group, register them yourself under the subscription's **Resource providers**.

The subscription and resource group are the connection's account. You can replace the secret, or switch to another principal with access to the same group, and nothing that uses the connection has to change.

## Point a manifest at it

```yaml
version: 1
defaults:
  provider: azure-container-apps
  region: westeurope
services:
  api:
    source: services/api
```

Or make the subscription a target in one of your Clouds, with its own region, and name that target in the manifest with `cloud_target`. One Cloud can mix it with targets on other providers and accounts.

The regions are `eastus`, `eastus2`, `centralus`, `westus2`, `westus3`, `canadacentral`, `brazilsouth`, `northeurope`, `westeurope`, `uksouth`, `francecentral`, `germanywestcentral`, `swedencentral`, `japaneast`, `koreacentral`, `southeastasia`, `centralindia` and `australiaeast`. Leave `region` out and you get `eastus`.

## What gets made

Everything lives in the connection's resource group, which Loomplane makes in the first region you deploy to if it isn't there:

- One Basic container registry, named from a hash of the subscription and resource group, since registry names are global across Azure. Its admin user stays on, even when your apps pull with the identity.
- The `loomplane-pull` managed identity, when the principal may assign roles.
- One Container Apps environment per region, called `loomplane-<region>`. It's consumption only, with no Log Analytics workspace.
- A container app for each service in each environment, named after the service, the environment and the project.

A service with a Dockerfile builds from it. Anything else goes through Google's buildpacks, run by `pack` in the same ACR Tasks run, the way Cloud Run and ECS build it. They handle Go, Node, Python, Java and more; a Python service's `start` command in the manifest becomes its entrypoint. Each deployment builds the commit the plan approved, tagged with its first twelve characters, and the app runs that image in a single active revision. A public service takes HTTPS traffic from anywhere on the app's own address. A private one only takes traffic from apps in its own Container Apps environment, which means the same connection and the same region. Services that need it get its internal address, and the plan stops with a finding if one of them sits in another region or another account. A service on another cloud can't reach it at all, so make it public if one needs it. `size` picks the smallest consumption size that fits, a quarter CPU at a time with twice as many GiB of memory, up to 4 CPUs and 8 GiB. `scale` sets the minimum and maximum replicas; without it Container Apps scales the app with traffic, down to zero.

A worker runs as a container app with no ingress, since it serves nothing. Nothing calls a worker to wake it, so it never scales to zero: the minimum is one replica even if you set `scale.min` to 0. A static site runs as a container app too, so it needs a Dockerfile that builds the site and serves the built files, with nginx or any other web server. Buildpacks don't make a file server, so the plan stops with a finding if a static site has no Dockerfile. Its address is public. The container app takes traffic on the port you set with `port:`, or the one your Dockerfile exposes, and otherwise on 8080 (the plan warns when it has to guess). A server that listens on 80, like nginx, needs `EXPOSE 80` in the Dockerfile or `port: 80` in the manifest.

Secret variables go in as app secrets, which Azure never shows again. The rest are plain environment variables.

Removing a service from the manifest deletes its container app. The registry and the environment stay for the group's other apps.

## Databases

| Manifest kind | Made on Azure | Provider | Versions |
| --- | --- | --- | --- |
| `postgres` | Azure Database for PostgreSQL flexible server | `azure-database` | 17, 16, 15 |
| `mysql` | Azure Database for MySQL flexible server | `azure-database` | 8.4, 8.0.21 |
| `redis` | Azure Managed Redis | `azure-managed-redis` | Azure's current one |

You place a database with `provider` and `region`, the same way as a service without a Cloud target:

```yaml
resources:
  db:
    kind: postgres
    version: "16"
    provider: azure-database
    region: westeurope
  cache:
    kind: redis
    provider: azure-managed-redis
    region: westeurope
```

Leave `version` out and you get Postgres 17 or MySQL 8.4. Each database goes in the connection's resource group, next to your apps. A service that needs one has to run on Azure in the same account, or the plan stops with a finding. It gets the database's address as an app secret.

Postgres and MySQL run on one `Standard_B1ms` Burstable server, with 32 GiB of storage for Postgres and 20 GiB for MySQL. The admin user is `loomplane`. Its password is 32 random characters that Loomplane makes the first time it applies the database. It keeps the password in Vault and sends the same one on every later apply, so it never changes. Each server has public network access and one firewall rule, `AllowAllAzureServicesAndResourcesWithinAzureIps`, which runs from `0.0.0.0` to `0.0.0.0`. That rule is how your container apps get in, and it lets in traffic from every other Azure customer's resources too, in any subscription. Only the password and TLS stand between them and your data. Loomplane leaves the servers' `require_secure_transport` parameter at Azure's default, which is on, and the addresses ask for TLS too, with `sslmode=require` for Postgres and `tls=true` for MySQL.

Redis is an Azure Managed Redis cluster on the `Balanced_B0` size, with one database called `default`. It only takes TLS connections, on port 10000, and it authenticates with an access key, so the address looks like `rediss://:<key>@<host>:10000`. The database uses Azure's enterprise clustering policy, which gives you a single endpoint, so a client that doesn't speak Redis Cluster works fine. Loomplane reads the key from Azure when it applies the cache, and keeps the address with the key in it as a secret. A deployment that doesn't change the cache doesn't apply it again. So if you regenerate the key in Azure, your apps keep the old one until the cache is applied again, and they can't connect in the meantime. Don't rotate the key there. Loomplane sets no network rule on the cluster, so its endpoint takes connections from anywhere that has the key. Managed Redis isn't offered in every region. If yours doesn't have it, the deployment fails with a message that names the region and suggests another one.

Server and cluster names are DNS names across all of Azure, so two customers can't both have `db-production`. Each name is built from the resource, the environment and the project, and ends in a hyphen and six hex characters of a hash of your subscription ID.

The databases need the `Microsoft.DBforPostgreSQL`, `Microsoft.DBforMySQL` and `Microsoft.Cache` resource providers. Loomplane registers each one on the first deployment that makes that kind of database, not when it checks the connection, so a connection that only runs apps stays ready without them. With Contributor on just a resource group, register them yourself under the subscription's **Resource providers**. The check does ask for the actions that make and read the databases, and Contributor has them all.

Removing a database from the manifest deletes it with its data, so take a backup first if you need one.

## What it can't do yet

Container Apps runs HTTP services, web apps, workers and static sites. Databases only have public endpoints for now. VNet integration and private endpoints aren't there yet, so the Azure services firewall rule is what lets your apps reach Postgres and MySQL. Every database gets the smallest size, and you can't pick another one yet. Build logs are under **Services**, **Tasks**, **Runs** in the container registry, and Loomplane shows the deployment's progress.
