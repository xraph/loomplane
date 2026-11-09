import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ProjectAutonomy } from "./project-autonomy"

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast: toasts }))

const state = vi.hoisted(() => ({ enabled: true, permissions: [] as string[] }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => false }),
}))
vi.mock("@/lib/features", () => ({
  useAgent: () => ({
    enabled: state.enabled,
    knowledge: false,
    loading: false,
    pending: 0,
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  state.enabled = true
  state.permissions = []
  toasts.success.mockClear()
  toasts.error.mockClear()
})

const project = {
  id: "proj_1",
  autonomousPromotion: false,
  minStagingSoakSeconds: 0,
} as Project

function serve() {
  const writes: { path: string; body: unknown }[] = []
  let calls = 0
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "PUT") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json({ id: "proj_1", autonomous_promotion: true })
      }
      if (path === "/api/v1/projects/proj_1/permissions") {
        calls++
        return Response.json({ permissions: state.permissions })
      }
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  return { writes, permissionReads: () => calls }
}

const renderIt = () =>
  render(
    <ClientProvider client={forgeClient}>
      <ProjectAutonomy project={project} />
    </ClientProvider>
  )

test("starts at ask first and needs approve permission", async () => {
  state.permissions = ["project:update"]
  const dev = serve()
  const view = renderIt()
  const toggle = await screen.findByRole("switch", {
    name: "Autonomous promotion",
  })
  expect(toggle).toHaveAttribute("aria-checked", "false")
  // The gate ships: the card lists the seven gates and what always asks.
  expect(
    screen.getByText("The agent promotes only when every gate holds")
  ).toBeInTheDocument()
  for (const gate of [
    "autonomous promotion",
    "promotes what staging runs",
    "clean plan",
    "staging soak",
    "safety net",
    "nothing open on staging",
    "budget",
  ])
    expect(screen.getByText(gate)).toBeInTheDocument()
  expect(
    screen.getByText(/Kubernetes, server, Vercel, DigitalOcean, Firecracker/)
  ).toBeInTheDocument()
  expect(
    screen.getByText(/a source's first production release/)
  ).toBeInTheDocument()
  expect(
    screen.getByText(/With it off, the agent still asks/)
  ).toBeInTheDocument()
  expect(screen.queryByText(/takes effect when the promotion/)).toBeNull()
  await waitFor(() => expect(dev.permissionReads()).toBe(1))
  expect(toggle).toHaveAttribute("aria-disabled", "true")
  await userEvent.click(toggle)
  expect(dev.writes).toEqual([])
  view.unmount()
  forgeClient.clear()

  // An admin can approve deployments, so the switch works.
  state.permissions = ["deployment:approve"]
  const admin = serve()
  renderIt()
  await waitFor(() =>
    expect(
      screen.getByRole("switch", { name: "Autonomous promotion" })
    ).not.toHaveAttribute("aria-disabled", "true")
  )
  await userEvent.click(
    screen.getByRole("switch", { name: "Autonomous promotion" })
  )
  await waitFor(() =>
    expect(admin.writes).toEqual([
      {
        path: "/api/v1/projects/proj_1/autonomy",
        body: { autonomous_promotion: true },
      },
    ])
  )
  await waitFor(() =>
    expect(toasts.success).toHaveBeenCalledWith(
      "Saved. The agent promotes a release someone asked for only when every gate holds."
    )
  )
})

test("offers the soak times, none to a day", async () => {
  state.permissions = ["deployment:approve"]
  serve()
  renderIt()
  const select = await screen.findByRole("combobox", {
    name: "Minimum staging soak",
  })
  await waitFor(() => expect(select).toBeEnabled())
  await userEvent.click(select)
  for (const label of [
    "None",
    "30 minutes",
    "1 hour",
    "2 hours",
    "6 hours",
    "12 hours",
    "24 hours",
  ])
    expect(await screen.findByRole("option", { name: label })).toBeVisible()
})

test("shows nothing while the agent is off", () => {
  state.enabled = false
  const dev = serve()
  const { container } = renderIt()
  expect(container).toBeEmptyDOMElement()
  expect(dev.permissionReads()).toBe(0)
})
