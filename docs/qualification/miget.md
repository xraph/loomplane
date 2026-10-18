# Miget qualification

Checked on 2026-10-18 against the public documentation. API base: `https://app.miget.com/api/v1`. The documentation exposes no immutable revision. This is a documentation review, with no live account, resource inventory or cost envelope supplied. G0 remains open. No Miget capability is enabled.

## Capability evidence

| Capability            | State       | Evidence and remaining proof                                                                                                                                     |
| --------------------- | ----------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `auth.workspace`      | unverified  | Documentation describes bearer workspace tokens and `X-Workspace-Id`. Capture a successful scoped read, wrong-workspace 403 and project-restricted denial.       |
| `lifecycle.observe`   | unverified  | `GET /apps` is documented. Confirm pagination, app and deployment IDs, readiness states and response envelope from a permitted test account.                     |
| `lifecycle.immutable` | unverified  | Registry documentation describes image URL and tag. Prove deployment by digest or an equivalent immutable revision.                                              |
| `lifecycle.control`   | unverified  | Disable Auto-Sync and confirm that replacement cannot race provider deployment automation. Capture exact write endpoints and recovery from an ambiguous timeout. |
| `secrets.deliver`     | unverified  | Prove secrets reach only the intended application and never appear in API/log output. Registry credentials need separate workspace administration.               |
| `logs.read`           | unverified  | Capture permitted log reads, redaction and authorization failures.                                                                                               |
| `identity.workload`   | unverified  | No qualified workload attestation or neighboring-application isolation evidence.                                                                                 |
| `identity.gateway`    | unverified  | Candidate fallback only. Prove isolated gateway credential delivery and backend bypass denial before use.                                                        |
| `dns.stable`          | unverified  | Documentation warns records can remain stale after replacement. Prove a supported non-disruptive refresh from both networks.                                     |
| `network.imported`    | unverified  | First integration target: an existing operator-managed path. Verify CIDRs, both address families and direction with active probes.                               |
| `network.ipsec`       | unverified  | Documented peer setup has a public-address allocation dependency. Capture the complete two-provider sequence before automating it.                               |
| `network.redundant`   | unsupported | Documentation describes one AWS tunnel; redundancy needs provider changes.                                                                                       |

Workspace tokens cannot use `/users/me`, administer integrations or create stored registry credentials. Use an application read for connection validation. Always pin the workspace header; never infer authorization from the token creator's role. These restrictions are documentation evidence, pending live tests. [API authentication](https://docs.miget.com/api-reference/introduction/).

Registry deployment accepts a tag and offers Auto-Sync. We need exact API request fields and immutable artifact proof before the lifecycle driver can deploy. [Registry deployment](https://docs.miget.com/deployments/container-registry/).

Attaching and detaching restart workloads. The attachment name can retain an old pod address after deployment, so an attach/detach workaround cannot qualify as a non-disruptive discovery refresh. Test IPv6 bypass as well as IPv4. [Subnets and attachments](https://docs.miget.com/networking/private-networks/subnets-and-attachments/).

IPsec creation takes the remote gateway, PSK and CIDRs before Miget assigns its public address. The remote side then needs that address. Deleting the tunnel leaves the gateway address allocated until the gateway is disabled. Record both cleanup handles; do not treat tunnel deletion as complete cleanup. [Site-to-site tunnels](https://docs.miget.com/networking/private-networks/site-to-site/).

## Capture contract

The fixture inventory under `internal/clouds/miget/testdata/qualification.json` contains requirements, not recorded API responses. The application listing reference could not be retrieved during this review, so pagination and endpoint-specific response shapes remain unknown. Do not invent them from the generic response envelope.

For each live capture, record the endpoint, method, request field names, status, response schema, pagination, scope, timestamp and sanitized resource correlation IDs. Replace tokens, PSKs, secret values and account details before committing. Keep a separate private cleanup inventory. Cover 401, wrong workspace 403, project denial, missing resource, rate limiting and an ambiguous create timeout.
