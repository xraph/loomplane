import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ResourceDetails } from "./resource-details"
vi.mock("@/lib/use-runs", () => ({
  useNow: () => new Date("2026-10-31T12:15:00Z").getTime(),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => (
    <span>{children}</span>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
const resource = {
  id: "r",
  name: "orders",
  provider: "digitalocean",
  connection_id: "c",
  account_identity: "Production",
  kind: "postgres",
  region: "nyc3",
  status: "online",
  ownership: "external",
  observed_at: "2026-10-31T12:00:00Z",
  endpoint_id: "",
  external_id: "db",
  revision: 1,
  observation_state: "observed",
  connection_state: "credentials_needed",
  credential_methods: ["dsn"],
}
const props = {
  id: "r",
  open: true,
  onOpenChange: () => {},
  canConnect: true,
  canReview: true,
  onAttach: () => {},
  onSaved: async () => {},
}
function fixture(
  options: {
    kind?: string
    fail?: boolean
    resource?: Record<string, unknown>
    success?: boolean
  } = {}
) {
  const writes: unknown[] = []
  vi.stubGlobal(
    "fetch",
    async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        writes.push(JSON.parse(String(init.body)))
        if (options.success)
          return Response.json({
            ...resource,
            ...options.resource,
            endpoint_id: "bucket-endpoint",
          })
        return Response.json(
          { message: "Secret access denied" },
          { status: 403 }
        )
      }
      return Response.json(
        options.resource
          ? { ...resource, ...options.resource }
          : options.kind
            ? { ...resource, kind: options.kind, credential_methods: [] }
            : resource
      )
    }
  )
  return writes
}
test("failed credential save stays visible and never claims readiness", async () => {
  const writes = fixture({ fail: true })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  await userEvent.type(
    await screen.findByLabelText("Application connection string"),
    "postgres://app:secret@db.example/orders"
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Save credentials" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Secret access denied"
  )
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Use in deployment" })
  ).toBeDisabled()
  expect(writes).toEqual([{ dsn: "postgres://app:secret@db.example/orders" }])
  expect(screen.getByLabelText("Application connection string")).toHaveValue("")
})

const bucket = {
  provider: "aws",
  name: "assets-production",
  kind: "object-storage",
  region: "us-east-1",
  external_id: "arn:aws:s3:::assets-production",
  bucket_owner: "123456789012",
  identity_scope: "account-name",
  credential_methods: ["bucket_access"],
}

test("S3 saves explicit bucket credentials without a database DSN", async () => {
  const writes = fixture({ resource: bucket, success: true })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  await userEvent.type(
    await screen.findByLabelText("Access key ID"),
    "AKIAFIXTUREACCESS01"
  )
  await userEvent.type(
    screen.getByLabelText("Secret access key"),
    "fixture-secret-key-12345"
  )
  await userEvent.type(
    screen.getByLabelText("Session token (optional)"),
    "fixture-session-token"
  )
  expect(
    screen.queryByLabelText("Application connection string")
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(/same account recreates this bucket/i)
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Save bucket credentials" })
  )
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Bucket credentials saved"
  )
  expect(writes).toEqual([
    {
      bucket_access: {
        provider: "aws",
        bucket: "assets-production",
        region: "us-east-1",
        expected_owner: "123456789012",
        access_key_id: "AKIAFIXTUREACCESS01",
        secret_access_key: "fixture-secret-key-12345",
        session_token: "fixture-session-token",
      },
    },
  ])
  expect(screen.getByLabelText("Secret access key")).toHaveValue("")
  expect(screen.getByLabelText("Session token (optional)")).toHaveValue("")
})

test("GCS uses an application service-account key and explains SDK initialization", async () => {
  const writes = fixture({
    resource: {
      ...bucket,
      provider: "gcp",
      identity_scope: "physical",
      bucket_owner: "",
      region: "us",
      external_id: "projects/project/buckets/assets-production",
    },
    success: true,
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  const key = '{"type":"service_account","private_key":"fixture-private-key"}'
  await userEvent.click(await screen.findByLabelText("Service account JSON"))
  await userEvent.paste(key)
  expect(screen.getByText(/GOOGLE_SERVICE_ACCOUNT_JSON/)).toBeInTheDocument()
  expect(screen.queryByLabelText("Access key ID")).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Save bucket credentials" })
  )
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Bucket credentials saved"
  )
  expect(writes).toEqual([
    {
      bucket_access: {
        provider: "gcp",
        bucket: "assets-production",
        region: "us",
        service_account_json: key,
      },
    },
  ])
  expect(screen.getByLabelText("Service account JSON")).toHaveValue("")
})

test("closing details clears unsaved application credentials", async () => {
  fixture({ resource: bucket })
  const view = (open: boolean) => (
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} open={open} />
    </ClientProvider>
  )
  const mounted = render(view(true))
  await userEvent.type(
    await screen.findByLabelText("Secret access key"),
    "unsaved-secret-key"
  )
  mounted.rerender(view(false))
  mounted.rerender(view(true))
  expect(await screen.findByLabelText("Secret access key")).toHaveValue("")
})

test("a failed refresh preserves the successful bucket credential outcome", async () => {
  fixture({ resource: bucket, success: true })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails
        {...props}
        onSaved={async () => {
          throw new Error("refresh unavailable")
        }}
      />
    </ClientProvider>
  )
  await userEvent.type(
    await screen.findByLabelText("Access key ID"),
    "AKIAFIXTUREACCESS01"
  )
  await userEvent.type(
    screen.getByLabelText("Secret access key"),
    "fixture-secret-key-12345"
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Save bucket credentials" })
  )
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Bucket credentials saved"
  )
  expect(
    await screen.findByText(
      /Bucket credentials were saved, but resource details could not be refreshed/
    )
  ).toBeInTheDocument()
  expect(screen.getByLabelText("Secret access key")).toHaveValue("")
})
test("unsupported credential methods never offer a database connection field", async () => {
  fixture({ kind: "object-storage" })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("heading", { name: "orders" })
  ).toBeInTheDocument()
  expect(
    screen.queryByLabelText("Application connection string")
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Use in deployment" })
  ).toBeDisabled()
})

test.each([
  [
    "cloudflare",
    "cloudflare-d1",
    "account-1",
    "database-1",
    "CLOUDFLARE_D1_DATABASE_ID",
  ],
  [
    "cloudflare",
    "cloudflare-kv",
    "account-1",
    "namespace-1",
    "CLOUDFLARE_KV_NAMESPACE_ID",
  ],
  [
    "cloudflare",
    "cloudflare-queue",
    "account-1",
    "queue-1",
    "CLOUDFLARE_QUEUE_ID",
  ],
  [
    "cloudflare",
    "cloudflare-vectorize",
    "account-1",
    "index-1",
    "CLOUDFLARE_VECTORIZE_INDEX",
  ],
  ["vercel", "vercel-blob", "team-1", "store-1", "BLOB_READ_WRITE_TOKEN"],
  ["vercel", "vercel-global-config", "team-1", "config-1", "GLOBAL_CONFIG"],
])(
  "%s %s saves a scoped application token using observed identity",
  async (provider, kind, account, externalId, output) => {
    const writes = fixture({
      resource: {
        provider,
        kind,
        account_identity: "display-identity",
        api_account_id: account,
        external_id: externalId,
        credential_methods: ["api_access"],
      },
      success: true,
    })
    render(
      <ClientProvider client={forgeClient}>
        <ResourceDetails {...props} />
      </ClientProvider>
    )
    const token = await screen.findByLabelText("Application API token")
    expect(token).toHaveAttribute("type", "password")
    expect(screen.getByText(account, { exact: true })).toBeInTheDocument()
    expect(screen.getByText(output, { exact: false })).toBeInTheDocument()
    await userEvent.type(token, "application-token")
    await userEvent.click(
      screen.getByRole("button", { name: "Save API credentials" })
    )
    expect(await screen.findByRole("status")).toHaveTextContent(
      "API credentials saved"
    )
    expect(writes).toEqual([
      {
        api_access: {
          provider,
          kind,
          account_id: account,
          resource_id: externalId,
          token: "application-token",
        },
      },
    ])
    expect(token).toHaveValue("")
    expect(
      screen.queryByLabelText("Application connection string")
    ).not.toBeInTheDocument()
  }
)

test("API credential denial clears the attempted secret and stays unbound", async () => {
  fixture({
    resource: {
      provider: "cloudflare",
      kind: "cloudflare-d1",
      account_identity: "display-identity",
      api_account_id: "account-1",
      external_id: "database-1",
      credential_methods: ["api_access"],
    },
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  const token = await screen.findByLabelText("Application API token")
  await userEvent.type(token, "rejected-token")
  await userEvent.click(
    screen.getByRole("button", { name: "Save API credentials" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Secret access denied"
  )
  expect(token).toHaveValue("")
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Use in deployment" })
  ).toBeDisabled()
})

test.each(["cloudflare-durable-object", "cloudflare-hyperdrive"])(
  "%s keeps native binding requirements visible even with an endpoint",
  async (kind) => {
    fixture({
      resource: {
        provider: "cloudflare",
        kind,
        credential_methods: [],
        endpoint_id: "old-endpoint",
        connection_state: "native_binding_required",
      },
    })
    render(
      <ClientProvider client={forgeClient}>
        <ResourceDetails {...props} />
      </ClientProvider>
    )
    expect(
      await screen.findByText(/requires a native Cloudflare Workers binding/i)
    ).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Use in deployment" })
    ).toBeDisabled()
  }
)

test("R2 saves S3 credentials against its observed Cloudflare account", async () => {
  const writes = fixture({
    resource: {
      ...bucket,
      provider: "cloudflare",
      account_identity: "display-identity",
      api_account_id: "account-1",
      scope: "accounts/account-1/r2/eu",
      bucket_owner: "account-1",
      region: "auto",
      external_id: "assets-production",
      identity_scope: "physical",
    },
    success: true,
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  await userEvent.type(
    await screen.findByLabelText("Access key ID"),
    "r2-access-key"
  )
  await userEvent.type(
    screen.getByLabelText("Secret access key"),
    "r2-secret-key"
  )
  expect(
    screen.queryByLabelText("Session token (optional)")
  ).not.toBeInTheDocument()
  expect(screen.getByText(/S3_ENDPOINT/)).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Save bucket credentials" })
  )
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Bucket credentials saved"
  )
  expect(writes).toEqual([
    {
      bucket_access: {
        provider: "cloudflare",
        account_id: "account-1",
        bucket: "assets-production",
        region: "auto",
        jurisdiction: "eu",
        access_key_id: "r2-access-key",
        secret_access_key: "r2-secret-key",
      },
    },
  ])
})

test("closing details clears an unsaved API token", async () => {
  fixture({
    resource: {
      provider: "cloudflare",
      kind: "cloudflare-d1",
      api_account_id: "account-1",
      external_id: "database-1",
      credential_methods: ["api_access"],
    },
  })
  const view = (open: boolean) => (
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} open={open} />
    </ClientProvider>
  )
  const mounted = render(view(true))
  await userEvent.type(
    await screen.findByLabelText("Application API token"),
    "unsaved-token"
  )
  mounted.rerender(view(false))
  mounted.rerender(view(true))
  expect(await screen.findByLabelText("Application API token")).toHaveValue("")
})

test("read-only access shows API outputs without offering credential writes", async () => {
  fixture({
    resource: {
      provider: "vercel",
      kind: "vercel-blob",
      credential_methods: ["api_access"],
      endpoint_id: "saved-endpoint",
    },
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} canConnect={false} canReview={false} />
    </ClientProvider>
  )
  expect(await screen.findByText(/BLOB_READ_WRITE_TOKEN/)).toBeInTheDocument()
  expect(
    screen.queryByLabelText("Application API token")
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Use in deployment" })
  ).toBeDisabled()
})

test("native binding access is explicit and sends no application or account token", async () => {
  const writes = fixture({
    resource: {
      provider: "cloudflare",
      kind: "cloudflare-hyperdrive",
      api_account_id: "account-1",
      external_id: "hyperdrive-1",
      credential_methods: ["native_binding"],
      connection_state: "native_binding_required",
    },
    success: true,
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  const native = await screen.findByRole("button", {
    name: "Use native binding",
  })
  expect(
    screen.queryByLabelText("Application API token")
  ).not.toBeInTheDocument()
  await userEvent.click(native)
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Native binding saved"
  )
  expect(writes).toEqual([
    {
      api_access: {
        provider: "cloudflare",
        kind: "cloudflare-hyperdrive",
        account_id: "account-1",
        resource_id: "hyperdrive-1",
        native: true,
      },
    },
  ])
})

test("Marketplace credentials require an explicit connected provider project and environment", async () => {
  const writes = fixture({
    resource: {
      provider: "vercel",
      kind: "vercel-integration",
      credential_methods: ["provider_credentials"],
    },
    success: true,
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  const project = await screen.findByLabelText("Existing Vercel project ID")
  const save = screen.getByRole("button", { name: "Save provider credentials" })
  expect(save).toBeDisabled()
  await userEvent.type(project, "prj_application")
  await userEvent.type(
    screen.getByLabelText("Provider environment"),
    "production"
  )
  await userEvent.click(save)
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Provider credentials saved"
  )
  expect(writes).toEqual([
    {
      provider_project_id: "prj_application",
      provider_environment: "production",
    },
  ])
  expect(
    screen.queryByLabelText("Application API token")
  ).not.toBeInTheDocument()
})
