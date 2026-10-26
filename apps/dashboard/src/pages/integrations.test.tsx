import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Integrations } from "@/pages/integrations"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture({ installed = true } = {}) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    let body: unknown = {}
    if (path === "/api/v1/github")
      body = {
        configured: true,
        install_url: "https://github.com/apps/loomplane/installations/new",
        installations: installed
          ? [
              {
                id: "1",
                account: "acme",
                account_type: "Organization",
                avatar_url: "",
                created_at: "2026-10-20T00:00:00Z",
                manage_url: "https://github.com/settings/installations/1",
              },
            ]
          : [],
      }
    if (path === "/api/v1/github/repositories")
      body = {
        repositories: [
          {
            full_name: "acme/web",
            default_branch: "main",
            installation: "1",
            private: true,
            url: "https://github.com/acme/web",
          },
          {
            full_name: "acme/docs",
            default_branch: "main",
            installation: "1",
            private: false,
            url: "https://github.com/acme/docs",
          },
        ],
      }
    if (path === "/api/v1/projects")
      body = {
        projects: [{ id: "p1", name: "Web app", repositories: ["acme/web"] }],
      }
    if (path === "/api/v1/connection-clouds")
      body = {
        clouds: [
          { cloud: "vercel", title: "Vercel", fields: [] },
          { cloud: "render", title: "Render", fields: [] },
        ],
      }
    if (path === "/api/v1/connections")
      body = {
        connections: [
          {
            id: "v",
            name: "Vercel team",
            cloud: "vercel",
            account: "acme-team",
            status: "ready",
          },
        ],
      }
    return Response.json(body)
  })
}

const show = () =>
  render(
    <ClientProvider client={forgeClient}>
      <Integrations tab="connected" onTab={() => {}} />
    </ClientProvider>
  )

it(
  "sums up what's connected and lists each GitHub repository's project",
  {
    timeout: 20_000,
  },
  async () => {
    fixture()
    show()
    expect(
      await screen.findByText("1 GitHub account and 1 cloud account")
    ).toBeInTheDocument()
    expect(screen.getByText("1 of 1")).toBeInTheDocument()
    expect(
      await screen.findByText("Loomplane can read, 1 private")
    ).toBeInTheDocument()

    const github = screen.getByRole("region", { name: "GitHub" })
    expect(within(github).getByText("acme")).toBeInTheDocument()
    expect(within(github).getByText("In Web app")).toBeInTheDocument()
    expect(within(github).getByText("Not in a project")).toBeInTheDocument()
    expect(
      within(github).getByRole("link", { name: "Open project" })
    ).toBeInTheDocument()

    await userEvent.type(
      within(github).getByRole("searchbox", { name: "Search repositories" }),
      "docs"
    )
    expect(within(github).queryByText("acme/web")).not.toBeInTheDocument()
    expect(within(github).getByText("acme/docs")).toBeInTheDocument()
  }
)

it("puts connected clouds first and offers Connect for the rest", async () => {
  fixture()
  show()
  const clouds = await screen.findByRole("region", { name: "Clouds" })
  expect(await within(clouds).findByText("Vercel team")).toBeInTheDocument()
  expect(within(clouds).getByText("Ready")).toBeInTheDocument()
  expect(
    within(clouds).getByRole("button", { name: "Add" })
  ).toBeInTheDocument()
  expect(
    within(clouds).getByRole("button", { name: "Connect" })
  ).toBeInTheDocument()
  expect(within(clouds).getByText("Not connected.")).toBeInTheDocument()
  // The logo tile carries the name too, so read the order from the rows.
  const rows = within(clouds)
    .getAllByRole("listitem")
    .map((row) => row.textContent)
  expect(rows[0]).toMatch(/^Vercel/)
  expect(rows[1]).toMatch(/^Render/)
})

it("asks for an install when no GitHub account is connected", async () => {
  fixture({ installed: false })
  show()
  expect(await screen.findByText("No GitHub accounts yet")).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: /Install on GitHub/ })
  ).toHaveAttribute(
    "href",
    "https://github.com/apps/loomplane/installations/new"
  )
})
