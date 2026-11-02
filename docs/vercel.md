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
