import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { CloudConnections } from "./cloud-connections"
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: { userId: "user" } }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
it("managed runtime connections cannot replace or remove renewable credentials", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("/connections"))
      return Response.json({
        connections: [
          {
            id: "managed",
            cloud: "kubernetes",
            name: "Project runtime",
            method: "managed_kubernetes",
            status: "ready",
            details: { managed_project_id: "project" },
            missing: [],
            created_at: "2026-10-29T00:00:00Z",
          },
        ],
      })
    return Response.json({ providers: [], clouds: [], members: [] })
  })
  render(
    <ClientProvider client={forgeClient}>
      <CloudConnections />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "Project runtime options" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Check again" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("menuitem", { name: "Replace credential" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("menuitem", { name: "Remove" })
  ).not.toBeInTheDocument()
})
