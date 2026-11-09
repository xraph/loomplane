import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { type ProjectTab, ProjectDetail } from "@/pages/project-detail"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/deploy", () => ({
  DeployTab: () => <p>Deploy tab</p>,
  ManifestSettings: () => <p>Manifest settings</p>,
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    className,
  }: {
    children: React.ReactNode
    to: string
    className?: string
  }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = {
  id: "p",
  name: "Shop",
  slug: "shop",
  status: "ready",
  description: "",
  repositories: [],
  exclusions: [],
  icon: "box",
  color: "blue",
  created_at: "2026-10-03T00:00:00Z",
  updated_at: "2026-10-03T00:00:00Z",
}

// open renders the page with a project that has no environments, the
// case where the Variables tab used to wait forever.
function open(tab?: ProjectTab) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    if (path === "/api/v1/projects/p") return json(project)
    if (path === "/api/v1/projects") return json({ projects: [project] })
    if (path.endsWith("/environments")) return json({ environments: [] })
    if (path.endsWith("/plans")) return json({ plans: [] })
    if (path.endsWith("/deployments")) return json({ deployments: [] })
    if (path.endsWith("/analyses")) return json({ analyses: [] })
    if (path.endsWith("/variables")) return json({ variables: [] })
    if (path.endsWith("/bindings")) return json({ bindings: [] })
    if (path.endsWith("/sources")) return json({ sources: [] })
    if (path.endsWith("/graph"))
      return json({ project_id: "p", excluded: [], sources: [] })
    return json({ members: [] })
  })
  render(
    <ClientProvider client={forgeClient}>
      <ProjectDetail id="p" tab={tab} />
    </ClientProvider>
  )
}

test("the header names the project, its state and the way to deploy", async () => {
  open()
  expect(
    await screen.findByRole("heading", { level: 1, name: "Shop" })
  ).toBeInTheDocument()
  expect(screen.getByText("Ready")).toBeInTheDocument()
  expect(screen.getByRole("banner")).toHaveTextContent("No repository yet")
  expect(screen.getByRole("link", { name: "Deploy" })).toHaveAttribute(
    "href",
    "/deployments/new"
  )
  const tabs = screen.getByRole("tablist", { name: "Project views" })
  expect(
    within(tabs)
      .getAllByRole("tab")
      .map((t) => t.textContent?.replace(/\d+$/, ""))
  ).toEqual([
    "Overview",
    "Services",
    "Deployments",
    "Variables",
    "Cloud",
    "Analyses",
    "Backups",
    "Settings",
  ])
})

test("variables with no environments asks for one instead of loading", async () => {
  open("variables")
  expect(await screen.findByText("No environments yet")).toBeInTheDocument()
  expect(
    screen.getAllByRole("button", { name: "Add environment" }).length
  ).toBeGreaterThan(0)
})

test("deleting the project asks for its name and says what runs", async () => {
  open("settings")
  const zone = await screen.findByRole("region", { name: "Danger zone" })
  expect(
    await within(zone).findByText(/Nothing runs from it yet/)
  ).toBeInTheDocument()
  const user = userEvent.setup()
  await user.click(within(zone).getByRole("button", { name: "Delete project" }))
  const dialog = await screen.findByRole("alertdialog")
  const confirm = within(dialog).getByRole("button", { name: "Delete project" })
  expect(confirm).toBeDisabled()
  await user.type(within(dialog).getByRole("textbox"), "Shop")
  expect(confirm).toBeEnabled()
})

test("the More menu holds copy and delete", async () => {
  open()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "More project actions" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Copy project ID" })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("menuitem", { name: "Delete project" })
  ).toBeInTheDocument()
})
