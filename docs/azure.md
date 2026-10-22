# Deploy to Azure Container Apps

Loomplane can build your services and run them on Azure Container Apps. It uploads the approved commit to a container registry in your subscription, ACR Tasks builds the service there, and a container app runs the image. Nothing from your repository runs on a Loomplane machine.

## Connect Azure

You connect a service principal: an application registered in Microsoft Entra ID, with a client secret.

1. In the Azure portal, open **Microsoft Entra ID**, then **App registrations**, and register an application. Note its **Application (client) ID** and **Directory (tenant) ID**.
2. Under **Certificates & secrets**, add a client secret and copy its value. Azure only shows it once.
3. On the subscription you'll deploy to, or on the resource group if you'd rather keep Loomplane inside one, open **Access control (IAM)** and give the application the **Contributor** role.
4. In Loomplane, open **Integrations**, choose **Microsoft Azure**, paste the secret, and fill in the tenant ID, client ID and subscription ID. The resource group is optional and defaults to `loomplane`.

If you prefer the CLI, `az ad sp create-for-rbac --role Contributor --scopes /subscriptions/<id>` does the first three steps at once: its `appId` is the client ID, `password` the secret and `tenant` the tenant ID.

Loomplane signs in as the principal before it keeps the secret. A wrong or expired secret is refused with Entra ID's own AADSTS code, so you can tell a mistyped secret from an expired one. Then it asks Azure what the principal's role assignments allow in the resource group, or in the subscription while the group isn't there yet, and lists every action a deployment needs that they leave out. A connection that lacks any is incomplete until you grant them and check it again. Contributor has them all.

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

- One Basic container registry, named from a hash of the subscription and resource group, since registry names are global across Azure. Its admin user is what your apps pull with.
- One Container Apps environment per region, called `loomplane-<region>`. It's consumption only, with no Log Analytics workspace.
- A container app for each service in each environment, named after the service, the environment and the project.

A service with a Dockerfile builds from it. Anything else goes through Google's buildpacks, run by `pack` in the same ACR Tasks run, the way Cloud Run and ECS build it. They handle Go, Node, Python, Java and more; a Python service's `start` command in the manifest becomes its entrypoint. Each deployment builds the commit the plan approved, tagged with its first twelve characters, and the app runs that image in a single active revision. A public service takes HTTPS traffic from anywhere on the app's own address. A private one only takes traffic from apps in its environment. `size` picks the smallest consumption size that fits, a quarter CPU at a time with twice as many GiB of memory, up to 4 CPUs and 8 GiB. `scale` sets the minimum and maximum replicas; without it Container Apps scales the app with traffic, down to zero.

A worker runs as a container app with no ingress, since it serves nothing. Nothing calls a worker to wake it, so it never scales to zero: the minimum is one replica even if you set `scale.min` to 0. A static site runs as a container app too, so it needs a Dockerfile that builds the site and serves the built files, with nginx or any other web server. Buildpacks don't make a file server, so the plan stops with a finding if a static site has no Dockerfile. Its address is public. The container app takes traffic on the port you set with `port:`, or the one your Dockerfile exposes, and otherwise on 8080 (the plan warns when it has to guess). A server that listens on 80, like nginx, needs `EXPOSE 80` in the Dockerfile or `port: 80` in the manifest.

Secret variables go in as app secrets, which Azure never shows again. The rest are plain environment variables.

Removing a service from the manifest deletes its container app. The registry and the environment stay for the group's other apps.

## What it can't do yet

Container Apps runs HTTP services, web apps, workers and static sites for now, but not databases. A private service only takes traffic from its own environment, and the planner doesn't give other services its address yet, so a service that needs one is asked to make it public. Build logs are under **Services**, **Tasks**, **Runs** in the container registry, and Loomplane shows the deployment's progress.
