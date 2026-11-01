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
function fixture(options: { kind?: string; fail?: boolean } = {}) {
  const writes: unknown[] = []
  vi.stubGlobal(
    "fetch",
    async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        writes.push(JSON.parse(String(init.body)))
        return Response.json(
          { message: "Secret access denied" },
          { status: 403 }
        )
      }
      return Response.json(
        options.kind
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
test("unsupported credential methods never offer a database connection field", async () => {
  fixture({ kind: "object-storage" })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceDetails {...props} />
    </ClientProvider>
  )
  expect(await screen.findByText("object-storage")).toBeInTheDocument()
  expect(
    screen.queryByLabelText("Application connection string")
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Use in deployment" })
  ).toBeDisabled()
})
