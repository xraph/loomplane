# Vercel resources

You can create Vercel Blob stores, Global Configs and Marketplace resources from **Resources**. Choose your account and project environment, fill in the provider options, then review and approve the deployment. Sync discovers existing stores and keeps their provider identity. An imported store remains externally owned when you attach or disconnect it.

Blob uses a store-scoped read/write token. Global Config uses a scoped read token. You can save these application credentials in the resource details, where values are write-only and kept in Vault.

## Marketplace and credential projects

Install the Marketplace integration in Vercel first. Creation needs its installation ID and product ID or slug; paid products may also need a billing plan. Your review retains these choices. Additional payment setup must be completed with Vercel. Use **Provisioning options (JSON)** for product-specific choices such as a region. These options must be a flat JSON object of non-secret values and remain visible in your deployment review.

Blob and Marketplace resources obtain static application credentials through a Vercel project connection. Choose a separate **credential bridge project ID** for this connection. The bridge project must belong to the same verified account and should contain no application builds: Vercel manages its resource variables there. Loomplane refuses to use the application's deployment project as that bridge.

For a Marketplace resource, `output_keys` lists the application variables to deliver, separated by commas. It defaults to `DATABASE_URL`. Choose the actual names the installed provider returns, such as `KV_URL` or `KV_REST_API_URL,KV_REST_API_TOKEN` for an applicable cache product. Unknown or missing outputs stop the deployment before it is recorded as successful.

When connecting an existing Marketplace store, select the bridge project and environment. Loomplane reads only that store connection's variables, verifies the current provider identity and saves the scoped application values. Portable connection checks support PostgreSQL, Redis and Upstash REST; other protocols remain blocked until a backing connection adapter is installed.

## Deployment use

Attach a resource to a running service through the same reviewed manifest flow. Resource credentials are sent to Vercel's runtime environment and excluded from its build environment. Existing project variables that would expose the linked credentials to a build must be removed or moved to a separate bridge project before deployment.

Other compatible deployment targets receive the resource's application variables. Cloud account administration tokens are never substituted for application credentials. Credentials, grants, bridge metadata and target identity are checked again on retries.

See [Vercel storage](https://vercel.com/docs/storage) for native product and Marketplace behavior.

## Deploy a monorepo as one project

You can deploy the services in a repository's `vercel.json` together. Choose the frontend's **Settings**, enable **Deploy as one Vercel Services project**, and set the project root to the directory containing that file. Use `.` for the repository root.

Keep one Loomplane manifest entry for the Vercel project. Its `source` selects the frontend found by analysis, so you can still manage its variables and deployment history. The Vercel project root is separate:

```yaml
version: 1
services:
  app:
    source: apps/web
    provider: vercel
    vercel:
      services: true
      root: .
      service_commands:
        web:
          install: pnpm install --frozen-lockfile
          build: pnpm --filter web build
```

Define the applications, routes and private bindings in that root's `vercel.json`:

```json
{
  "services": {
    "web": { "root": "apps/web" },
    "api": { "root": "services/api", "framework": "fastapi" }
  },
  "rewrites": [
    { "source": "/api/(.*)", "destination": { "service": "api" } },
    { "source": "/(.*)", "destination": { "service": "web" } }
  ]
}
```

Remove separate Loomplane entries for the members when you want them deployed only inside this project. Review that plan carefully if those entries already have running deployments: their removal follows Loomplane's normal deletion flow.

The group shares one deployment, environment, URL and rollback history. Retrying resumes that deployment. Variables and resource links belong to the group; configure private member-to-member calls through Vercel bindings. The complete pinned repository is uploaded, including shared packages outside the project root.

Use the current `services` configuration. The older `experimentalServices` format and `vercel.ts` are not supported by this mode. A missing configuration, unknown member command override or top-level build setting stops the deployment. Vercel determines which member frameworks and runtimes it can build.

## Override install and build commands

In a Vercel application's **Settings**, choose an environment and select **Repository default**, **Custom** or **Skip** for each command. In Services mode, enter the exact member name from `vercel.json` before changing its commands.

For a standalone application, the equivalent manifest is:

```yaml
services:
  web:
    source: apps/web
    provider: vercel
    commands:
      install: pnpm install --frozen-lockfile
      build: pnpm build
    environments:
      preview:
        commands:
          build: pnpm build:preview
```

An environment's `commands` or `vercel` block replaces the corresponding base block. Omit a command to keep its repository or provider default. Set it to `""` to skip that step. An empty `commands: {}` restores repository defaults for both commands in that environment.

Overrides change only the uploaded copy of `vercel.json`; your repository is not rewritten. They take effect on the next reviewed deployment and are retained with its configuration for retries and rollback. Keep secrets in Variables because commands appear in manifest history and deployment review. These overrides currently require the Vercel provider.

See [Vercel Services](https://vercel.com/docs/services) and the [service configuration reference](https://vercel.com/docs/services/config-reference) for routing, bindings and runtime options.
