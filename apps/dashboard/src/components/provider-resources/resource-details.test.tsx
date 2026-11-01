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
