# Octopus in a project

Octopus is an API gateway. You add it to a project, point your services at it, and it routes requests to them by hostname and path. Some routes it builds itself from your services' API schemas, over FARP. The rest you add on its Routes tab, and Loomplane pushes them to Octopus while it runs, so a route change never needs a redeploy.

## Add Octopus

Open **Services**, choose **Octopus**, then pick the project, the environment and where it runs: a checked Docker server or a Kubernetes cluster. Give the resource a name, keep version 0.5 and review the plan before you approve it.

You don't need a domain for any of this. Until you attach one, Octopus answers on the target's private network only, and the services in the environment reach it at its private address on port 8080. A service that lists the resource in `needs` gets that address in `OCTOPUS_URL`.

The same thing in the manifest looks like this:

```yaml
resources:
  edge:
    kind: octopus
    provider: server
    target: conn_01hx...
services:
  billing:
    source: services/billing
    provider: server
    target: conn_01hx...
    port: 8080
    farp:
      openapi: /openapi.json
```

Octopus runs as one stateless container or pod, `ghcr.io/xraph/octopus` 0.5.0, pinned by digest for amd64 and arm64, with no volume. Port 8080 is the gateway and the only port that's ever routed publicly. Port 9090 serves the admin dashboard, the admin API and FARP, and only Loomplane reaches it, over the target's private channel.

Loomplane generates the admin password, the admin session secret, an admin API token and a FARP token, keeps all four in Vault and hands them to Octopus as secrets. The admin always requires a password. You never need to see it, because **Open Octopus admin** signs you in.

Anyone who can read the project sees the gateway's tabs. Adding, changing or removing a route, attaching a domain, picking where FARP routes land and pushing again all need permission to modify the project's infrastructure, which admins and owners have.

## Routes

The Routes tab shows every route on the gateway, grouped by hostname, wherever it came from:

| Source | Where it comes from |
|---|---|
| FARP | Octopus built it from a service's schema. The note says which one, such as "From the OpenAPI schema". |
| Added here | You added it on this tab. It shows whether Octopus holds it yet. |
| Octopus admin | Somebody made it in the Octopus admin. It won't survive a restart, so add it here instead. |

Choose **Add route** and fill in the domain, the path, the methods and the service. The domain is one of the hostnames attached to the gateway, **Any hostname**, or **Attach another domain…**, which attaches a new hostname from the same dialog. Leave the methods empty and the route takes any method. **Send to** lists the services in the environment that have an HTTP port and run on the same server or cluster as Octopus, because Octopus reaches them by their private address. A note is optional and at most 120 characters.

Loomplane saves the route, then pushes it to Octopus over the admin API within 10 seconds. If another push holds the gateway, it waits for up to 5 seconds before giving up, and it never fails your change: the route is saved either way, and the dialog tells you whether Octopus has it, whether it's still pending, or what Octopus refused. Anything pending goes out with the next reconcile, within 30 seconds.

A path starts with `/` and matches exactly: `/auth` answers `/auth` and nothing under it. End it with `/*` to match everything underneath, so `/auth/*` answers `/auth/login`, `/auth/x/y` and `/auth` itself. Octopus's own `/auth/*` doesn't match `/auth`, so Loomplane gives the route a second entry for the bare prefix. A path can't end in a slash (drop it, or use `/*`), can't hold `?`, `#`, `//`, `.` or `..` segments, and puts `*` only at the end. Paths are at most 512 characters. **Strip** removes the prefix before the request goes on, so `/auth/login` reaches the service as `/login`. It isn't offered on `/` or `/*`, where there's nothing to strip.

Two routes on the same hostname can't share a path, and `/auth/*` can't sit beside `/auth`, since it already answers it. Edit the route that's there instead.

A route on **Any hostname** answers whatever hostname a request carries, including the gateway's private address. That's how you use Octopus with no domain at all.

### Overriding a FARP route

A route you add beats a FARP route on the same hostname and path. Select **Override** on a FARP row and the dialog opens with that hostname and path filled in. Pick where it should go and save. Your row then shows **Overrides FARP**, and the FARP route stays hidden for as long as yours exists. Delete yours and the FARP route comes back on the next push.

FARP never takes priority over a route you added, and a service republishing its schema doesn't touch your routes either, because Loomplane only ever changes or removes the routes it put in Octopus itself.

### When a route can't be pushed

Loomplane keeps a route it can't serve and says why on its row. The service isn't deployed in this environment. It moved to another server or cluster. It listens on another port now, in which case the row says "billing now listens on 9000. Edit the route to send it there." Octopus can also refuse a route, most often because somebody made a route for the same path in the Octopus admin. Remove that one in the admin, or change yours.

Loomplane names what it puts in Octopus after your routes and services. A route is `lp-<route id>`, plus `lp-<route id>-base` for the bare prefix of a `/*` path, and the service port it sends to is an upstream named `lp-<service>-<port>`. You'll see those names in the Octopus admin. Don't edit them there: the next push puts them back.

## Domains

On the Domains tab, choose **Attach a domain** and pick a hostname under a zone your organization has granted to the project, either exact, like `api.acme.dev`, or a wildcard, like `*.acme.dev`. Each hostname is one Loomplane domain route to the gateway on port 8080, with its DNS record and certificate handled the way [domains.md](domains.md) describes. Octopus does the path routing behind it. Every domain route to Octopus refuses `/__farp`, `/_farp` and `/admin` before the request reaches it, so FARP's endpoints and the admin dashboard are never public.

**Detach** removes the hostname's DNS record and stops serving it. It's blocked while routes you added still use that hostname: move or delete them first. You also detach every domain before you remove Octopus itself, by taking it out of the manifest and deploying.

### Where FARP routes land

FARP routes answer on every hostname until you pick where they land. Each attached hostname has a **FARP routes land here** switch, and while nothing is picked every one of them shows on. Turn one off and the others stay picked, so from then on FARP routes answer only on those, which keeps a schema's routes off a hostname you meant for something else. Switching a hostname off never widens FARP routes to every hostname. After that you can turn hostnames on and off as you like, except the last one that's on. FARP routes have to land somewhere, so turn on another one first.

Detaching doesn't widen them either. When you detach the only hostname picked for FARP and others stay attached, the confirm warns you first. Loomplane keeps that hostname as FARP's pick, and since it no longer routes to Octopus, FARP routes answer only on the target's private network until you pick another hostname. The same goes for a gateway whose picked hostnames are all detached. The Routes, Discovery and Domains tabs then say FARP routes land on a hostname that isn't attached any more, and every switch shows off. Turn one on and FARP routes land there alone.

## FARP discovery

A service opts in with a `farp` block in the manifest. [manifest.md](manifest.md#farp-discovery) lists its keys. Octopus reads the schemas the block names and builds the service's routes from them, and the Discovery tab shows what it found.

### On Kubernetes

Octopus watches the cluster itself. Loomplane gives it a ServiceAccount and a Role in the connection's namespace that can get, list and watch `services`, `endpoints` and `endpointslices`, and nothing else. [security.md](security.md#octopus-discovery-access) covers what that lets it see.

A service that opts in gets two labels on its Kubernetes Service, `farp.enabled=true` and `loomplane.farp.gateway=<resource>`, and its block as annotations: `farp.openapi.path`, `farp.asyncapi.path`, `farp.graphql.path`, `farp.health.path`, `farp.metrics.path`, `farp.grpc.reflection`, `farp.capabilities` and `farp.port`, each one only when it's set. Octopus selects by `farp.enabled=true`, the environment and the gateway's name, so two Octopus resources in one namespace don't pick up each other's services. It reads the endpoints from EndpointSlices, skips ones that aren't ready, and checks again every 30 seconds.

### On a Docker server

A Docker server has nothing for Octopus to watch, so Loomplane registers each opted-in service with it instead. The registration is `lp-svc-<service>`, at the service's container name on the environment's network and its FARP port, with the block's metadata. Loomplane registers a service again on every release, and the reconcile sends each one a heartbeat every 30 seconds. When Octopus answers a heartbeat with 404, or has restarted, Loomplane registers the service again in the same pass. A service that drops its `farp` block or is removed is deregistered.

The Discovery tab shows each service as Registered, Waiting for a release or Failed, with the reason. **Register again** runs a push now. It registers any service Octopus doesn't hold, and sends the others a heartbeat.

## Open Octopus admin

**Open Octopus admin** sits on the gateway's page and needs permission to modify the project's infrastructure. It opens a new tab on Loomplane's own address, under `/catalog-admin/<token>/admin`, already signed in. Your browser never holds the admin password or the API token.

The tab goes through Loomplane's admin proxy. The proxy reaches port 9090 over the target's private channel: SSH to the port Docker publishes on the server's loopback interface, or the Kubernetes API server's service proxy. It adds Octopus's admin API token to each request and forwards only paths under `/admin`. Everything it sends back runs sandboxed in an opaque origin, so the admin's pages can't read Loomplane's cookies or call Loomplane's API as you, and Octopus's own cookies and security headers are dropped on the way. A session ends after 30 minutes without use and 8 hours after you opened it, whichever comes first. The proxy checks at least once a minute that you still have permission, and removing Octopus ends every session open on it.

Routes you make in the admin work until Octopus restarts, and then they're gone, since Loomplane doesn't know about them and has nothing to put back. The Routes tab marks them as made in the admin. Add a route on the Routes tab when you want to keep it.

## Restarts

Octopus starts from `/etc/octopus/config.yaml`, which Loomplane renders on every deploy from the environment's services, your routes and the FARP hostnames. The only secret it mentions is the FARP token, and only by the placeholder `${OCTOPUS_FARP_TOKEN}` that Octopus fills in from its environment. A route you add between deploys doesn't change the file or restart Octopus. Loomplane pushes it over the admin API, and the next deploy writes it into the file.

What's pushed at runtime goes away when Octopus restarts, so a reconcile job puts it back. It runs every 30 seconds and syncs every gateway, eight at a time, giving each sync 10 seconds. A sync compares Octopus's start time with the one it saw last. When they differ, Octopus has restarted, and the sync puts back your routes and their upstreams, the FARP hostnames and, on a Docker server, every service's FARP registration. A restart therefore costs you at most about 30 seconds of the routes you added since the last deploy.

The reconcile is also what catches anything else a write didn't get through, such as a push made while Octopus was down.

## Troubleshooting

### The Settings tab shows a sync error

The Sync section on the Settings tab shows when Loomplane last pushed to Octopus, "Pushing to Octopus" while a change is still on its way, and the last push's error. Each error says what Loomplane does next:

| Message | What's going on |
|---|---|
| Octopus isn't answering on its private port. | The container or pod is down, or the private channel can't reach it. Loomplane keeps your change and pushes it within 30 seconds. |
| Octopus isn't running in this environment yet. | The first deploy hasn't finished. The push happens once it has. |
| Loomplane is already pushing this gateway's routes. | Another push was running. Yours goes out within 30 seconds. |
| Loomplane couldn't read the gateway's admin credentials. | The generated secrets aren't readable from Vault. Loomplane tries again within 30 seconds. |
| Octopus refused Loomplane's admin requests. | Octopus answered the admin API with an error instead of its routes. Loomplane tries again within 30 seconds. |
| Octopus didn't accept one of this gateway's routes. | The route's own row says why. |

**Push routes again** runs a push now. When Octopus doesn't answer within 5 seconds, the Routes tab still lists your routes and says FARP's can't be shown until it's back.

### `catalog-rbac-unavailable` on a Kubernetes plan

Octopus needs its discovery Role, and the cluster connection can't create one. Let the connection create `serviceaccounts`, `roles` and `rolebindings` in its namespace and get, list and watch `services`, `endpoints` and `endpointslices`, then choose **Check again** on the connection and plan again.

A plan can also stop on `catalog-channel-unavailable`. Loomplane reaches Octopus's port 9090 through the cluster's service proxy, so the connection needs get and create on `services/proxy` in its namespace.

### `farp-*` problems on a plan

| Problem | Fix |
|---|---|
| `farp-gateway-missing` | The service opts in to FARP and the manifest has no Octopus resource. Add one, or drop the block. |
| `farp-gateway-ambiguous` | The manifest has two or more Octopus resources. Set `farp.gateway` on the service. |
| `farp-gateway-unknown` | `farp.gateway` names something that isn't an Octopus resource. |
| `farp-port-missing` | Nothing says which port the service listens on. Set its `port` or `farp.port`. |
| `farp-target-mismatch` | The service and its gateway run on different servers or clusters. FARP needs them on the same one. |
| `farp-name-reserved` | The service's name starts with `lp-`, which Loomplane's upstreams use. Rename it. |

### A FARP route is missing

Check the Discovery tab first. A service stays Waiting for a release until it's deployed with its `farp` block. On a Docker server, a Failed service shows the status Octopus answered with; fix the service and choose **Register again**. Then check where FARP routes land on the Domains tab: a FARP route only answers on the hostnames picked there, or on every hostname when none is. When every hostname picked there has been detached, FARP routes answer only on the target's private network.
