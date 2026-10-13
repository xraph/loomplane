import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { act, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { createDashboardRouter } from "./router"

// Exercise the actual generated routes without starting an auth session or
// making API requests. Authentication is checked separately in the browser.
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => <p>Incomplete invitation</p> }
})
vi.mock("@/pages/projects", async () => {
  const { Link } = await import("@tanstack/react-router")
  return {
    Projects: () => <Link to="/members">Open members</Link>,
  }
})
vi.mock("@/pages/members", () => ({ Members: () => <h1>Members</h1> }))
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))
vi.mock("@/pages/deployments", () => ({
  runFilters: [
    { value: "all" },
    { value: "live" },
    { value: "waiting" },
    { value: "failed" },
    { value: "succeeded" },
  ],
  Deployments: ({ status }: { status: string }) => (
    <h1>Deployments {status}</h1>
  ),
}))
vi.mock("@/pages/deployment", () => ({
  DeploymentPage: ({ id }: { id: string }) => <h1>Deployment {id}</h1>,
}))
vi.mock("@/pages/project-detail", () => ({
  ProjectDetail: ({ id }: { id: string }) => <h1>Project {id}</h1>,
}))

beforeEach(() => {
  // jsdom has no scrolling; the router restores it in the browser.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => vi.restoreAllMocks())

async function open(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] })
  const router = createDashboardRouter(history)
  await router.load()
  render(<RouterProvider router={router} />)
  return { router, history }
}

describe("dashboard routes", () => {
  it("opens a project deep link and decodes its ID", async () => {
    await open("/projects/project%20one")
    expect(
      await screen.findByRole("heading", { name: "Project project one" })
    ).toBeInTheDocument()
  })

  it("encodes project IDs as a single path parameter", async () => {
    const { router } = await open("/")
    await act(async () => {
      await router.navigate({
        to: "/projects/$id",
        params: { id: "team/project?draft" },
      })
    })
    expect(router.state.location.pathname).toBe(
      "/projects/team%2Fproject%3Fdraft"
    )
    expect(
      await screen.findByRole("heading", { name: "Project team/project?draft" })
    ).toBeInTheDocument()
  })

  it("opens the overview at the root", async () => {
    await open("/")
    expect(
      await screen.findByRole("heading", { name: "Overview" })
    ).toBeInTheDocument()
  })

  it("filters deployments by a known status", async () => {
    await open("/deployments?status=waiting")
    expect(
      await screen.findByRole("heading", { name: "Deployments waiting" })
    ).toBeInTheDocument()
  })

  it("shows every deployment for an unknown status", async () => {
    await open("/deployments?status=bogus")
    expect(
      await screen.findByRole("heading", { name: "Deployments all" })
    ).toBeInTheDocument()
  })

  it("opens a deployment by its plan ID", async () => {
    await open("/deployments/plan_123")
    expect(
      await screen.findByRole("heading", { name: "Deployment plan_123" })
    ).toBeInTheDocument()
  })

  it("navigates with links and browser back and forward", async () => {
    const user = userEvent.setup()
    const { history } = await open("/projects")
    await user.click(await screen.findByRole("link", { name: "Open members" }))
    expect(
      await screen.findByRole("heading", { name: "Members" })
    ).toBeInTheDocument()
    await act(async () => history.back())
    expect(
      await screen.findByRole("link", { name: "Open members" })
    ).toBeInTheDocument()
    await act(async () => history.forward())
    expect(
      await screen.findByRole("heading", { name: "Members" })
    ).toBeInTheDocument()
  })

  it("shows a recoverable missing page instead of the projects list", async () => {
    const user = userEvent.setup()
    await open("/missing/page")
    expect(
      await screen.findByRole("heading", { name: "Page not found" })
    ).toBeInTheDocument()
    await user.click(screen.getByRole("link", { name: "Back to projects" }))
    expect(
      await screen.findByRole("link", { name: "Open members" })
    ).toBeInTheDocument()
  })

  it("retains invitation tokens in the URL", async () => {
    const { router } = await open("/invitations/accept?token=invite%2Bone")
    expect(router.state.location.search).toEqual({ token: "invite+one" })
    expect(await screen.findByText("Incomplete invitation")).toBeInTheDocument()
  })
})
