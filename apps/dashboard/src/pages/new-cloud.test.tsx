import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"

import { NewOrganizationCloud } from "./clouds"

const navigate = vi.fn()
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => navigate,
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  navigate.mockReset()
})

function fixture() {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if ((init?.method ?? "GET") !== "GET") {
        const body = JSON.parse(String(init?.body ?? "{}"))
        writes.push({ path, body })
        return Response.json(
          { id: "cloud_new", name: body.name, project_id: "", revision: 1 },
          { status: 201 }
        )
      }
      if (path === "/api/v1/providers")
        return Response.json({
          providers: [
            {
              name: "vercel",
              cloud: "vercel",
              title: "Vercel",
              available: true,
              runs: ["service"],
              regions: ["iad1", "fra1"],
            },
          ],
        })
      if (path === "/api/v1/connections")
        return Response.json({
          connections: [
            {
              id: "v1",
              name: "Team Vercel",
              cloud: "vercel",
              account: "team",
              status: "ready",
            },
          ],
        })
      return Response.json({})
    }
  )
  return { writes }
}

const show = () =>
  render(
    <ClientProvider client={forgeClient}>
      <NewOrganizationCloud open onOpenChange={() => undefined} />
    </ClientProvider>
  )

it("creates a Cloud by name alone", async () => {
  const user = userEvent.setup()
  const { writes } = fixture()
  show()
  await user.type(screen.getByRole("textbox", { name: "Cloud name" }), "EU")
  await user.click(screen.getByRole("button", { name: "Create Cloud" }))
  await waitFor(() =>
    expect(writes).toEqual([{ path: "/api/v1/clouds", body: { name: "EU" } }])
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/clouds/$id",
    params: { id: "cloud_new" },
  })
})

it("creates a Cloud with its one default provider and account", async () => {
  const user = userEvent.setup()
  const { writes } = fixture()
  show()
  await user.type(screen.getByRole("textbox", { name: "Cloud name" }), "Edge")
  await user.click(
    screen.getByRole("checkbox", { name: /Start with a default provider/ })
  )
  // Ticked but unchosen, the Cloud can't be created yet.
  expect(screen.getByRole("button", { name: "Create Cloud" })).toBeDisabled()
  await user.click(await screen.findByRole("radio", { name: /Vercel/ }))
  await user.click(screen.getByRole("radio", { name: /Team Vercel/ }))
  await user.click(screen.getByRole("combobox", { name: "Default region" }))
  await user.click(await screen.findByRole("option", { name: /fra1/ }))
  await user.click(screen.getByRole("button", { name: "Create Cloud" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        path: "/api/v1/clouds",
        body: {
          name: "Edge",
          default_placement: {
            provider: "vercel",
            connection_id: "v1",
            region: "fra1",
          },
        },
      },
    ])
  )
})
