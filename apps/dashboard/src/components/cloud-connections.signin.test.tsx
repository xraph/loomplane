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

const assign = vi.fn()
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  assign.mockReset()
})

type Posted = { path: string; body: Record<string, unknown> }

function stub(methods: string[], posted: Posted[] = [], connection?: object) {
  vi.stubGlobal("location", { ...window.location, assign })
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        posted.push({
          path,
          body: JSON.parse(String(init.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        return Response.json({
          authorizeUrl: "https://cloud.example/authorize",
        })
      }
      if (path.includes("connection-clouds"))
        return Response.json({
          clouds: [
            {
              cloud: "digitalocean",
              title: "DigitalOcean",
              builtin: false,
              credential: "API token",
              methods,
            },
          ],
        })
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

function dialog(cloud = "digitalocean") {
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud={cloud} onOpenChange={vi.fn()} />
    </ClientProvider>
  )
}

it("offers sign-in first when the cloud supports it", async () => {
  stub(["oauth", "token"])
  dialog()
  expect(
    await screen.findByRole("button", { name: "Sign in with DigitalOcean" })
  ).toBeTruthy()
  expect(screen.getByLabelText(/^Name$/i)).toBeTruthy()
  expect(screen.queryByLabelText(/API token/i)).toBeNull()
  expect(screen.queryByRole("button", { name: "Check and connect" })).toBeNull()
})

it("keeps the token form one click away", async () => {
  stub(["oauth", "token"])
  dialog()
  const user = userEvent.setup()
  await user.click(await screen.findByText("Use a token instead"))
  expect(screen.getByLabelText(/API token/i)).toBeTruthy()
  expect(screen.getByRole("button", { name: "Check and connect" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: /Sign in with/ })).toBeNull()
})

it("looks like today for a token-only cloud", async () => {
  stub(["token"])
  dialog()
  expect(await screen.findByLabelText(/API token/i)).toBeTruthy()
  expect(screen.getByRole("button", { name: "Check and connect" })).toBeTruthy()
  expect(screen.queryByText("Use a token instead")).toBeNull()
  expect(screen.queryByRole("button", { name: /Sign in with/ })).toBeNull()
})

it("starts a sign-in and goes to the cloud", async () => {
  const posted: Posted[] = []
  stub(["oauth", "token"], posted)
  dialog()
  const user = userEvent.setup()
  await user.type(await screen.findByLabelText(/^Name$/i), "  Team DO ")
  await user.click(
    screen.getByRole("button", { name: "Sign in with DigitalOcean" })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith("https://cloud.example/authorize")
  )
  expect(posted).toHaveLength(1)
  expect(posted[0]!.path).toContain("sign-in")
  expect(posted[0]!.body).toEqual({ cloud: "digitalocean", name: "Team DO" })
})

it("offers sign in again for a lapsed connection", async () => {
  const posted: Posted[] = []
  stub(["oauth", "token"], posted, {
    id: "conn_do",
    name: "Team DO",
    cloud: "digitalocean",
    status: "sign_in_needed",
    message: "The sign-in lapsed.",
    details: {},
    missing: [],
    createdAt: "2026-10-01T00:00:00Z",
    createdBy: "u1",
  })
  render(
    <ClientProvider client={forgeClient}>
      <CloudAccount id="conn_do" />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.click(await screen.findByRole("button", { name: "Sign in again" }))
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith("https://cloud.example/authorize")
  )
  expect(posted[0]!.body).toEqual({
    cloud: "digitalocean",
    name: "",
    connection_id: "conn_do",
  })
})
