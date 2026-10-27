# Domains

Bring a domain you already own, or register a new one through your own Cloudflare account, then give your services hostnames under it. Loomplane writes the DNS record, gets the certificate and keeps both up to date. Everything lives on the **Domains** page, between Services and Clouds in the sidebar.

## Bring a domain

Open **Domains**, choose **Bring a domain** and type the domain you own. You then pick who runs its DNS.

If Cloudflare, DigitalOcean or Vercel already hosts the zone, choose **Loomplane manages its DNS** and pick the connected account. Loomplane reads the zone from that account, checks the name and the account match, and marks it verified straight away. From then on it creates and removes the records for your routes. It only ever changes records it created. [Cloudflare tokens](#cloudflare-tokens) explains how it tells them apart on Cloudflare.

If you'd rather keep DNS where it is, choose **Verify with TXT**. Loomplane gives you a TXT record to add, named `_loomplane.<your domain>` with a value starting `loomplane=`. The value is good for 24 hours. Add it with whoever hosts your DNS, then select **Check again**. Once the record shows up, the domain is verified. You add each hostname's record yourself too: the route tells you the exact record, and Loomplane checks it before it calls the hostname ready.

A domain belongs to the organization. Grant it to the projects that may use it, and only those projects can put hostnames under it. Someone needs organization cloud management access to bring, grant or remove a domain.

## Register a domain

You can buy a new domain without leaving Loomplane. It's bought by, billed to and owned by your own Cloudflare account. Loomplane never resells domains, and you can manage the domain in Cloudflare like any other.

1. Connect Cloudflare under **Integrations** with a token that has the Registrar permission (see [Cloudflare tokens](#cloudflare-tokens)).
2. On **Domains**, choose **Register a domain** and search for a name. The extensions you can pick come from Cloudflare's own list, so they change when Cloudflare's do.
3. Pick a name. Loomplane asks Cloudflare for its live price and shows it. That quote is held for 15 minutes. After that, you check the price again before you can buy.
4. Tick the line that says Cloudflare charges the account's payment method, then choose **Buy**. That approves the exact price on the button and nothing else.

Right before it buys, Loomplane checks the price with Cloudflare again. If it moved, nothing is bought, the registration ends `price_changed` and you're asked to review the new price. If Cloudflare refuses the purchase outright, it ends `failed` with Cloudflare's status and error codes, and the message says "Nothing was bought."

A purchase can also go missing in transit: Cloudflare got the request, but the answer never arrived. Loomplane never sends a second purchase for the same registration. It asks Cloudflare for that name's registration status instead, and keeps asking. If an hour goes by and Cloudflare still has no record of it, the registration ends `unconfirmed` and asks you to check your Cloudflare dashboard.

Once the domain is registered, Cloudflare activates its zone and Loomplane adds it as a managed domain, ready for hostnames. While Cloudflare finishes that, the registration shows `zone_pending`. Your WHOIS contact details are redacted unless you turn that off. If you give contact details of your own, they're kept in Vault and deleted when the registration ends. Otherwise Cloudflare uses the default contact on the account.

You'll hear about the outcome once, in your inbox and by email, whether it's registered, failed, needs action in Cloudflare, was blocked, changed price or couldn't be confirmed.

### Auto-renew and the renewal notice

Cloudflare leaves auto-renew off by default. Loomplane turns it **on** when it registers a domain, because a domain that lapses takes your hostnames with it, and the Domains page always shows the current setting.

Thirty days before a domain renews, while auto-renew is on, everyone in the organization who can manage clouds gets a notice, in the inbox and by email. That email goes out even if your deployment emails are off. It gives the renewal date, the price if Cloudflare reports one, and a link to turn auto-renew off.

The link doesn't change anything by itself. It opens the Domains page with the **Turn off auto-renew** button in focus, and you decide there. You can turn auto-renew back on the same way.

Domain notices reach the inbox and email only. Webhook endpoints subscribe to deployment events today, so they don't receive domain events yet.

### Try it in the sandbox

Cloudflare has a Registrar sandbox that never charges anyone and creates no zone. Point Loomplane at it in `config.yaml` (or with `LOOMPLANE_CLOUDFLARE_REGISTRAR_SANDBOX=true`):

```yaml
clouds:
  cloudflare_registrar_sandbox: true
```

Registrar calls then go to `/accounts/{account_id}/registrar-sandbox/...`, and the register dialog marks itself **Sandbox**. Development and CI always use it. DNS has no sandbox, though. The zones API is your real account, so Loomplane never creates a zone for a sandbox registration: it stays at `zone_pending` with "Sandbox registrations don't create a DNS zone." That's as far as it can go, unless you already have a zone with that name in the account, in which case Loomplane adopts it the way it would any other.

Loomplane reads the setting when it starts. If you switch it, select **Check again** on each Cloudflare connection before you buy anything. Until you do, a preview won't quote a price, because the connection still records the old mode. The review step takes its **Sandbox** label and "Nothing is charged" from the registrar Loomplane is running against, and a purchase approved under one mode ends `failed` under the other with nothing bought.

Transfers into Cloudflare aren't supported.

## Cloudflare tokens

Create an API token in Cloudflare, then open **Integrations**, choose **Cloudflare**, paste the token and enter your account ID, which is 32 lowercase letters and digits.

| Permission | What it's for |
|---|---|
| Zone, Zone, Read | Listing your zones. Required. |
| Zone, DNS, Edit | Writing route records. Required. |
| Account, Registrar, Edit | Registering domains. Optional. |

Loomplane checks each permission without changing anything. It lists zones, sends a DNS record that Cloudflare can only refuse, and asks to edit the registration of `loomplane-permission-probe.invalid`, a name nobody can ever register. Then it reads whether each refusal is about permission. Registrar access is optional, so a token without it still connects. It just can't buy. If Cloudflare is rate limiting or having trouble when Loomplane asks about the registrar, you'll see that permission as unchecked while DNS keeps working, and **Check again** asks a second time. If the token can't read account details, the connection is named after the account ID.

Every record Loomplane writes is DNS-only (`proxied: false`) and carries Loomplane's owner token in its comment. That comment is how it knows a record is its own. A record you made yourself is never touched, and preview refuses a hostname that one of your records already holds. Loomplane never adopts a proxied record it didn't create. If you later switch on the orange cloud for a record Loomplane did create, the next write sets it back to DNS-only.

### The DNS challenge token

Wildcard certificates are proven through DNS, so the server or cluster that serves the wildcard needs a token of its own. That token has to live on the machine itself, so it's a second, much narrower token and never your main one.

Create a second Cloudflare token with Zone Read and DNS Edit on that one zone, and nothing else. Connect it under Integrations like the first. Then open the domain on the Domains page and choose **Set up wildcard certificates**. Loomplane refuses the token unless:

- it's a different connection from the one that manages the zone, in the same Cloudflare account,
- no other zone uses it,
- the only zone it can see is this one, and it can edit DNS there,
- it has no Registrar access,
- it's in the format Caddy's Cloudflare module accepts (Loomplane checks this first, before it asks Cloudflare anything).

These checks run when you attach it and again every time Loomplane writes the token to a server or cluster. So if someone widens the token in Cloudflare later, the next write refuses it. See [security.md](security.md#dns-challenge-credentials) for where it ends up.

## Kubernetes routes

A service on a Kubernetes connection can take hostnames under any domain your project is granted, with the same DNS records and receipts as a server. Loomplane makes an Ingress for each hostname and asks cert-manager for the certificate.

You'll need cert-manager 1.12 or later in the cluster. The HTTP-01 solver relies on `ingress.ingressClassName`, which arrived in 1.12. The connection check looks for the cert-manager API and reports `route_prerequisites` as `ready` or `missing: ...`. A cluster that doesn't use routes can skip all this, and its connection stays ready either way.

Two connection settings matter for routes:

- `ingress_address` is where your ingress controller receives traffic, as an IP address or a hostname. Routes need it. An IP gives an A or AAAA record. A hostname gives a CNAME, which DNS can't put at the zone apex, so an apex route on a hostname address is refused.
- `ingress_class` names the ingress class for Loomplane's Ingresses and the HTTP-01 solver. Leave it out to use the cluster's default class.

DNS is never pointed at an address the Ingress doesn't report. If your controller reports a different address from `ingress_address`, the route stays `provisioning` and the message names both addresses, so you can fix one or the other.

Loomplane only creates objects in the connection's namespace, never anything cluster-scoped:

- the Issuer `lp-acme-http01`, and `lp-acme-dns01-<hash>` for each zone with a wildcard route,
- a Certificate and an Ingress (`lp-route-<hash>`) for each hostname,
- one shared Service, `lp-route-deny`, with no endpoints, for blocked paths,
- one Secret per wildcard zone, holding its DNS challenge token under `api-token`.

The ACME account has no email address, so nothing about you reaches the certificate authority. Certificates come from Let's Encrypt. Set `clouds.acme_directory` (or `LOOMPLANE_ACME_DIRECTORY`) to another HTTPS directory, such as Let's Encrypt staging, to test without touching the production rate limits.

The service account in your kubeconfig needs these permissions on top of the ones workloads need. Bind them in the namespace you connect:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: loomplane-routes
  namespace: shop
rules:
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "create", "update", "patch", "delete"]
  - apiGroups: ["cert-manager.io"]
    resources: ["issuers", "certificates"]
    verbs: ["get", "list", "create", "update", "patch", "delete"]
  - apiGroups: ["acme.cert-manager.io"]
    resources: ["orders", "challenges"]
    verbs: ["get", "list"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["services", "secrets"]
    verbs: ["get", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: loomplane-routes
  namespace: shop
subjects:
  - kind: ServiceAccount
    name: loomplane
    namespace: shop
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: loomplane-routes
```

Replace `shop` and `loomplane` with your namespace and the service account the kubeconfig's token belongs to.

You can keep updating a service that has a Kubernetes route. A plan that would replace or recreate it is refused until you remove the route, because the new runtime gets a new Service the old Ingress doesn't follow.

### Blocked paths

A route can keep paths off its public hostname, such as `/admin` on a gateway. Each prefix starts with `/`, has no trailing slash, and can't hold `//`, `%`, `*`, `?`, `#`, or a `.` or `..` segment. You can block up to 8, of up to 128 bytes each. Blocked paths answer 404 on servers and 503 on Kubernetes, where they go to `lp-route-deny`.

## Wildcards

A wildcard hostname looks like `*.preview.acme.dev`. The `*` has to be the whole first label, and there can only be one. You can put wildcards under a domain that is:

- managed by Loomplane on Cloudflare,
- verified,
- set up with a [DNS challenge token](#the-dns-challenge-token).

An exact hostname always wins over a wildcard that covers it. If `api.acme.dev` routes to Shop and `*.acme.dev` routes to Docs, `api.acme.dev` keeps reaching Shop, on servers and on Kubernetes alike.

On Kubernetes, the zone's token goes into a namespaced Secret that a DNS-01 Issuer reads, and that Issuer only answers for that zone. When the last wildcard route in the namespace that uses them goes, the Issuer and the Secret go too.

On a server, the proxy needs Loomplane's DNS-capable image, `ghcr.io/xraph/loomplane-ingress`. A server still on the stock image is upgraded with its first wildcard route, and the preview warns you first:

> Every HTTPS hostname on {server} is unavailable for a few seconds while Loomplane restarts its proxy with DNS challenge support. Certificates and settings stay in their volumes.

That image isn't published yet. Until it is, a wildcard preview on a server says so and goes no further. Wildcards on Kubernetes work today.

## Route states

Every hostname shows three states, one each for DNS, the certificate and the route itself:

| State | Meaning |
|---|---|
| `awaiting_verification` | The domain isn't verified yet, or it's a TXT-verified domain and Loomplane is waiting for the record you add. |
| `provisioning` | Loomplane is writing the record, waiting for the certificate, or waiting for the backend to answer. |
| `ready` | Done and checked. |
| `failed` | The last attempt failed. The message says why, and you can retry. |
| `renewing` | The certificate is being reissued, or expires within 14 days. The hostname still serves. |
| `cleanup_pending` | The route is being removed. |

## Over HTTP

Domain routes run through the same preview and apply as the rest of Loomplane: preview, read what it will do, then apply with the review token it gave you.

```sh
curl -X POST .../api/v1/projects/prj_.../domain-routes/preview \
  -d '{"zone_id":"zone_...","environment_id":"env_...","service":"web","hostname":"app.acme.dev","port":8080}'
curl -X POST .../api/v1/projects/prj_.../route-operations/routeop_.../apply \
  -d '{"review":"..."}'
curl .../api/v1/projects/prj_.../domain-routes/route_.../status
```

| Route | Operation |
|---|---|
| `GET /domains` | `domains.overview` |
| `GET /projects/:projectId/domains` | `domains.project` |
| `GET, POST /dns-zones` | `dnsZones.list`, `dnsZones.create` |
| `GET /dns-zones/discover?connection_id=` | `dnsZones.discover` |
| `POST /dns-zones/:zoneId/verify` | `dnsZones.verify` |
| `POST /dns-zones/:zoneId/challenge-credential` | `dnsZones.setChallengeCredential` |
| `GET /projects/:projectId/domain-routes` | `domainRoutes.list` |
| `POST /projects/:projectId/domain-routes/preview` | `domainRoutes.preview` |
| `POST /projects/:projectId/route-operations/:operationId/apply` | `domainRoutes.apply` |
| `POST /projects/:projectId/route-operations/:operationId/retry` | `domainRoutes.retry` |
| `GET /projects/:projectId/route-operations/:operationId` | `domainRoutes.operation` |
| `GET /projects/:projectId/domain-routes/:routeId/status` | `domainRoutes.status` |
| `POST /projects/:projectId/domain-routes/:routeId/verify` | `domainRoutes.verify` |
| `POST /projects/:projectId/domain-routes/:routeId/remove` | `domainRoutes.remove` |
| `GET /domain-registration/extensions?connection_id=` | `domainRegistration.extensions` |
| `GET /domain-registration/search?connection_id=&q=` | `domainRegistration.search` |
| `POST /domain-registration/check` | `domainRegistration.check` |
| `POST /domain-registrations/preview` | `domainRegistrations.preview` |
| `POST /domain-registrations/:registrationId/approve` | `domainRegistrations.approve` |
| `GET /domain-registrations` | `domainRegistrations.list` |
| `GET /domain-registrations/:registrationId` | `domainRegistrations.get` |
| `POST /domain-registrations/:registrationId/auto-renew` | `domainRegistrations.setAutoRenew` |

Every path sits under `/api/v1`. Approval takes the `review` token from the preview and echoes the quoted `currency` and `registration_cost`. It answers 202, and you follow the registration until it settles. A route to a server needs the service placed through a Cloud. A Kubernetes service can also run on the environment's own connection, with no Cloud at all.

## The acceptance run

`TestAcceptanceDomains` checks all of this against the real thing, in two parts that skip separately. Each part names the variables it's missing and skips before it opens a database or sends a request.

The registrar part needs `LOOMPLANE_CF_SANDBOX_TOKEN` and `LOOMPLANE_CF_ACCOUNT_ID`, a Cloudflare token with Registrar access and its account. It runs Loomplane in sandbox mode behind a guard that refuses any production registrar path, any transfer request and any write to the account's zones. Then it checks the token's permissions, lists extensions, searches, checks, previews and approves a `.com` with auto-renew on, waits for `succeeded` or `zone_pending`, turns auto-renew off and reads it back.

The route part needs a real zone and a disposable cluster on top of those two:

| Variable | What it is |
|---|---|
| `LOOMPLANE_ACCEPTANCE_ZONE` | A Cloudflare zone set aside for testing. The sandbox token needs Zone Read and DNS Edit on it. |
| `LOOMPLANE_ACCEPTANCE_KUBECONFIG` | A kubeconfig for a disposable kind cluster with cert-manager 1.12 or later and an ingress controller. |
| `LOOMPLANE_ACCEPTANCE_CF_CHALLENGE_TOKEN` | A second token with Zone Read and DNS Edit on that zone only. |
| `LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS` | The address the internet reaches the ingress controller at, on ports 80 and 443. |
| `LOOMPLANE_ACCEPTANCE_KUBE_CONTEXT`, `_KUBE_NAMESPACE`, `_INGRESS_CLASS` | Optional connection settings. |
| `LOOMPLANE_ACCEPTANCE_TIMEOUT` | How long each wait may take. 30m by default. |

Let's Encrypt staging connects to the cluster on port 80 to check the exact hostname's HTTP-01 challenge, and the controller has to report the same address on its Ingresses. A kind cluster on a laptop behind NAT can't do that. Run it on a machine with a public address.

The run deploys a small service to the cluster, adds an exact and a wildcard hostname under the zone, waits for DNS, certificate and route to be `ready` on both, removes them and reads the zone back from Cloudflare to confirm none of Loomplane's records are left.

```bash
LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' \
LOOMPLANE_CF_SANDBOX_TOKEN=... LOOMPLANE_CF_ACCOUNT_ID=... \
LOOMPLANE_ACCEPTANCE_ZONE=acceptance.example.com LOOMPLANE_ACCEPTANCE_KUBECONFIG=$PWD/kind.kubeconfig \
LOOMPLANE_ACCEPTANCE_CF_CHALLENGE_TOKEN=... LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS=203.0.113.10 \
  go test -tags='integration acceptance' -count=1 -timeout=120m -v -run '^TestAcceptanceDomains$' ./tests/integration
```

Give `go test` a `-timeout` longer than three times `LOOMPLANE_ACCEPTANCE_TIMEOUT` plus the registrar's 10-minute poll: 120m covers the 30m default. When go test runs out of time it panics, and a panic skips the cleanups, which leaves records in your real zone. So the run also stops its own waits 15 minutes before that deadline, and the cleanups get what's left.
