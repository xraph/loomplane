import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { CloudAccount } from "@/pages/cloud-account"
import { ConnectDialog } from "./cloud-connections"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { orgId: "org", userId: "u1" },
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => vi.fn(),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

type Posted = { path: string; method: string; body: Record<string, unknown> }

const forms: Record<string, object> = {
  aws: {
    cloud: "aws",
    title: "AWS",
    builtin: true,
    credential: "Secret access key",
    methods: ["keyless", "token"],
    keyless_settings: [{ key: "role_arn", label: "Role ARN", required: true }],
  },
  gcp: {
    cloud: "gcp",
    title: "Google Cloud",
    builtin: true,
    credential: "Service account key",
    methods: ["oauth", "keyless", "token"],
    keyless_settings: [
      { key: "workload_provider", label: "Workload provider", required: true },
    ],
  },
  vercel: {
    cloud: "vercel",
    title: "Vercel",
    builtin: true,
    credential: "API token",
    methods: ["token"],
  },
}

const setup = {
  connection_id: "conn_k1",
  issuer: "https://loomplane.example",
  subject: "org:org_1:connection:conn_k1",
  audience: "sts.amazonaws.com",
  steps: [{ title: "Make the role", body: "Trust the provider." }],
}

function stub(posted: Posted[] = [], connection?: object) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST" || init?.method === "PUT") {
        posted.push({
          path,
          method: init.method,
          body: JSON.parse(String(init.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        return Response.json(setup)
      }
      if (path.includes("connection-clouds"))
        return Response.json({ clouds: Object.values(forms) })
      if (path.includes("/keyless")) return Response.json(setup)
      if (path.includes("members")) return Response.json({ members: [] })
      if (connection && /connections\/[^/?]+$/.test(path))
        return Response.json(connection)
      return Response.json({
        clouds: [],
        connections: [],
        bindings: [],
        accounts: [],
        sources: [],
        totals: [],
      })
    })
  )
}

function dialog(cloud: string) {
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud={cloud} onOpenChange={vi.fn()} />
    </ClientProvider>
  )
}

it("offers keyless first for AWS", async () => {
  stub()
  dialog("aws")
  expect(
    await screen.findByRole("button", { name: "Connect keylessly" })
  ).toBeTruthy()
  expect(screen.getByText("Use an access key instead")).toBeTruthy()
  expect(screen.queryByLabelText(/Secret access key/i)).toBeNull()

  const user = userEvent.setup()
  await user.click(screen.getByText("Use an access key instead"))
  expect(screen.getByLabelText(/Secret access key/i)).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Connect keylessly" })).toBeNull()
})

it("offers keyless as a link under sign-in", async () => {
  stub()
  dialog("gcp")
  expect(
    await screen.findByRole("button", { name: "Sign in with Google Cloud" })
  ).toBeTruthy()
  const link = screen.getByText("Connect keylessly instead")
  expect(screen.getByText("Use a token instead")).toBeTruthy()
  // The keyless link comes before the token link.
  expect(
    link.compareDocumentPosition(screen.getByText("Use a token instead")) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()

  const user = userEvent.setup()
  await user.click(link)
  expect(await screen.findByText("https://loomplane.example")).toBeTruthy()
  expect(screen.getByLabelText("Workload provider")).toBeTruthy()
})

it("looks like today for a cloud without keyless", async () => {
  stub()
  dialog("vercel")
  expect(await screen.findByLabelText(/Access token/i)).toBeTruthy()
  expect(screen.queryByText(/keylessly/i)).toBeNull()
})

it("switches an existing connection to keyless", async () => {
  const posted: Posted[] = []
  stub(posted, {
    id: "conn_a",
    name: "Prod AWS",
    cloud: "aws",
    method: "token",
    status: "ready",
    message: "",
    account: "123456789012",
    details: { region: "eu-west-1" },
    missing: [],
    createdAt: "2026-10-01T00:00:00Z",
    createdBy: "u1",
  })
  render(
    <ClientProvider client={forgeClient}>
      <CloudAccount id="conn_a" />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "Switch to keyless" })
  )
  expect(await screen.findByText("https://loomplane.example")).toBeTruthy()
  await user.type(screen.getByLabelText("Role ARN"), "arn:aws:iam::1:role/x")
  await user.click(screen.getByRole("button", { name: "Check and switch" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0]!.method).toBe("PUT")
  expect(posted[0]!.path).toContain("/connections/conn_a/keyless")
  expect(posted[0]!.body).toEqual({
    settings: { role_arn: "arn:aws:iam::1:role/x" },
  })
})

it("says a connection is connected keylessly", async () => {
  stub([], {
    id: "conn_a",
    name: "Prod AWS",
    cloud: "aws",
    method: "keyless",
    status: "ready",
    message: "",
    details: {},
    missing: [],
    createdAt: "2026-10-01T00:00:00Z",
    createdBy: "u1",
  })
  render(
    <ClientProvider client={forgeClient}>
      <CloudAccount id="conn_a" />
    </ClientProvider>
  )
  expect(await screen.findByText("Connected keylessly")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Switch to keyless" })).toBeNull()
})

it("replaces a keyless connection's credential with a key, not keyless again", async () => {
  stub()
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog
        open
        cloud="aws"
        onOpenChange={vi.fn()}
        replacing={
          {
            id: "conn_k",
            name: "Prod AWS",
            cloud: "aws",
            method: "keyless",
            status: "ready",
            message: "",
            account: "123456789012",
            details: { region: "eu-west-1", role_arn: "arn:aws:iam::1:role/x" },
            missing: [],
            createdAt: "2026-10-01T00:00:00Z",
            createdBy: "u1",
          } as unknown as Parameters<typeof ConnectDialog>[0]["replacing"]
        }
      />
    </ClientProvider>
  )
  expect(await screen.findByLabelText(/Secret access key/i)).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Connect keylessly" })).toBeNull()
  expect(screen.queryByText("Connect keylessly instead")).toBeNull()
})
