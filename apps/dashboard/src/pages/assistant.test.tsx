import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AgentProvider, FeaturesProvider } from "@/lib/features"
import { createDashboardRouter } from "@/router"
import { FakeEventSource } from "@/test-fixtures/event-source"

const BUSY =
  "The agent is still on your last message. Wait for it to finish, answer what it asked, or stop it."

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openNewProject: () => {}, openShortcuts: () => {} }),
}))
vi.mock("@/lib/use-runs", async (original) => ({
  ...(await original<typeof import("@/lib/use-runs")>()),
  useRuns: () => ({ runs: [] }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
// The routes and their pages, beside the sidebar; not the rest of the shell.
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  const { AppSidebar } = await import("@/components/app-sidebar")
  const { SidebarProvider } = await import("@/components/ui/sidebar")
  return {
    default: () => (
      <SidebarProvider>
        <AppSidebar />
        <main>
          <Outlet />
        </main>
      </SidebarProvider>
    ),
    Problem: () => null,
  }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))

type Write = { method: string; path: string; body: unknown }
type Route = (body: unknown) => Response

let writes: Write[] = []
let reads: string[] = []

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  writes = []
  reads = []
})

const conversation = {
  id: "conv_1",
  org_id: "org",
  project_id: "",
  title: "What's deployed?",
  started_by: "usr_1",
  status: "queued",
  last_task_id: "atask_1",
  turns: 1,
  created_at: "2026-11-03T10:00:00Z",
  updated_at: "2026-11-03T10:00:00Z",
}

const task = {
  id: "atask_1",
  org_id: "org",
  project_id: "",
  environment_id: "",
  kind: "chat",
  run_id: "arun_1",
  session_id: "conv_1",
  status: "queued",
  requested_by: "usr_1",
  max_tokens: 0,
  tokens: 0,
  event_seq: 0,
  error: "",
  metadata: {},
  created_at: "2026-11-03T10:00:00Z",
  updated_at: "2026-11-03T10:00:00Z",
  finished_at: null,
}

function request(over: Record<string, unknown> = {}) {
  return {
    id: "areq_1",
    org_id: "org",
    project_id: "",
    environment_id: "",
    task_id: "atask_1",
    run_id: "arun_1",
    kind: "answer",
    status: "pending",
    question: "Which region should the cache live in?",
    context: [],
    options: ["eu-west-1", "us-east-1"],
    target: {},
    asked_for: "usr_1",
    answered_by: "",
    answer: "",
    outcome: "",
    team_id: "",
    team_name: "",
    route_source: "asker",
    team_reason: "",
    route_note: "",
    expires_at: "2026-11-04T10:00:00Z",
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    answered_at: null,
    ...over,
  }
}

function serve(agentOn: boolean, routes: Record<string, Route> = {}) {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method === "GET") reads.push(url.pathname)
      else writes.push({ method, path: url.pathname, body })
      const route = routes[`${method} ${url.pathname}`]
      if (route) return route(body)
      if (url.pathname === "/api/v1/agent/status")
        return Response.json({
          server: true,
          enabled: agentOn,
          knowledge: false,
        })
      if (url.pathname === "/api/v1/agent/conversations")
        return Response.json({ conversations: [] })
      if (url.pathname === "/api/v1/agent/requests")
        return Response.json({ requests: [] })
      return Response.json({ projects: [], members: [], installations: [] })
    }
  )
}

const started: Record<string, Route> = {
  "POST /api/v1/agent/conversations": () =>
    Response.json({ conversation, task }, { status: 201 }),
}

function open(path: string, ai = true) {
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: [path] })
  )
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai }}>
        <AgentProvider>
          <RouterProvider router={router} />
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
  return router
}

let seq = 0
function frame(kind: string, data: Record<string, unknown> = {}) {
  seq += 1
  FakeEventSource.latest().emit(
    "agent.event",
    { task_id: "atask_1", seq, kind, data, at: "2026-11-03T10:00:00Z" },
    seq
  )
}

async function ask(text: string) {
  const box = await screen.findByRole(
    "textbox",
    { name: "Message the agent" },
    { timeout: 15_000 }
  )
  await userEvent.type(box, `${text}{Enter}`)
  await waitFor(() =>
    expect(FakeEventSource.instances.length).toBeGreaterThan(0)
  )
  seq = 0
}

it("hides the assistant when the server runs no agent", async () => {
  serve(true)
  open("/assistant", false)
  // The first route to load pays for the page's modules.
  expect(
    await screen.findByText("Page not found", {}, { timeout: 15_000 })
  ).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Overview" })).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull()
  expect(reads.filter((p) => p.includes("/agent/"))).toEqual([])
})

it("hides the assistant when the workspace's agent is off", async () => {
  serve(false)
  open("/assistant")
  expect(
    await screen.findByText("Page not found", {}, { timeout: 15_000 })
  ).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull()
  expect(
    reads.filter((p) => p.startsWith("/api/v1/agent/conversations"))
  ).toEqual([])
})

it("starts a conversation and streams the answer", async () => {
  serve(true, started)
  const router = open("/assistant")
  // The sidebar offers it, right after Overview.
  const links = (await screen.findAllByRole("link"))
    .map((a) => a.textContent?.trim())
    .filter((t) => t === "Overview" || t === "Assistant" || t === "Deployments")
  expect(links.slice(0, 2)).toEqual(["Overview", "Assistant"])
  expect(
    await screen.findByText(/Ask about your projects, deployments and releases/)
  ).toBeInTheDocument()

  await ask("What's deployed?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: { message: "What's deployed?" },
  })
  expect(FakeEventSource.latest().url).toMatch(
    /\/api\/v1\/agent\/tasks\/atask_1\/stream\?after=0$/
  )
  frame("run_started")
  frame("step")
  frame("token", { text: "Two " })
  frame("token", { text: "**projects**." })
  expect(await screen.findByText("projects")).toHaveProperty(
    "tagName",
    "STRONG"
  )
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  await waitFor(() =>
    expect(router.state.location.search).toEqual({ c: "conv_1" })
  )
  // The stream that answered is the one still shown.
  expect(screen.getByText("projects")).toBeInTheDocument()
  expect(screen.getByText("What's deployed?")).toBeInTheDocument()
})

it("shows a tool step with its risk", async () => {
  serve(true, started)
  open("/assistant")
  await ask("Ship the release")
  frame("step")
  frame("tool_call", {
    tool: "approve_plan",
    call_id: "call_1",
    risk: "promote_production",
    arguments: { plan_id: "plan_9" },
  })
  const row = await screen.findByRole("button", { name: /approve_plan/ })
  expect(within(row).getByText("Production release")).toBeInTheDocument()
  expect(within(row).getByText("Running")).toBeInTheDocument()
  expect(screen.queryByText(/plan_9/)).toBeNull()
  await userEvent.click(row)
  expect(await screen.findByText(/"plan_id": "plan_9"/)).toBeInTheDocument()
  frame("tool_result", {
    tool: "approve_plan",
    call_id: "call_1",
    outcome: "ok",
  })
  expect(await within(row).findByText("Done")).toBeInTheDocument()
})

it("answers the agent's question inline", async () => {
  serve(true, {
    ...started,
    "GET /api/v1/agent/requests/areq_1": () => Response.json(request()),
    "POST /api/v1/agent/requests/areq_1/answer": () =>
      Response.json(request({ status: "answered", answer: "eu-west-1" })),
  })
  open("/assistant")
  await ask("Set up a cache")
  frame("step")
  frame("token", { text: "I need to know one thing." })
  frame("request", { request_id: "areq_1", kind: "answer" })
  frame("suspended", { reason: "waiting" })
  expect(
    await screen.findByText("Which region should the cache live in?")
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "eu-west-1" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "POST",
      path: "/api/v1/agent/requests/areq_1/answer",
      body: { text: "eu-west-1" },
    })
  )
})

it("keeps one message in flight", async () => {
  serve(true, {
    ...started,
    "POST /api/v1/agent/tasks/atask_1/cancel": () =>
      Response.json({ ...task, status: "cancelled" }),
  })
  open("/assistant")
  await ask("Deploy staging")
  frame("run_started")
  frame("step")
  frame("token", { text: "On it." })
  const box = screen.getByRole("textbox", { name: "Message the agent" })
  expect(box).toBeDisabled()
  await userEvent.click(screen.getByRole("button", { name: "Stop" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "POST",
      path: "/api/v1/agent/tasks/atask_1/cancel",
      body: undefined,
    })
  )
  frame("status", { status: "cancelled" })
  FakeEventSource.latest().emit("agent.end", { status: "cancelled" })
  expect(await screen.findByText("Stopped.")).toBeInTheDocument()
  await waitFor(() => expect(box).toBeEnabled())
})

it("shows why a message was refused", async () => {
  serve(true, {
    "POST /api/v1/agent/conversations": () =>
      Response.json({ message: BUSY }, { status: 409 }),
  })
  open("/assistant")
  const box = await screen.findByRole("textbox", { name: "Message the agent" })
  await userEvent.type(box, "Deploy staging{Enter}")
  expect(await screen.findByRole("alert")).toHaveTextContent(BUSY)
  // The message goes back in the box, and off the page.
  expect(box).toHaveValue("Deploy staging")
  expect(
    screen.getByText(/Ask about your projects, deployments and releases/)
  ).toBeInTheDocument()
  expect(FakeEventSource.instances).toHaveLength(0)
})

it("renders a blocked answer as withheld", async () => {
  serve(true, started)
  open("/assistant")
  await ask("Show me the AWS key")
  frame("step")
  frame("token", { text: "Here it is: AKIA" })
  frame("token", { text: "IOSFODNN7EXAMPLE" })
  expect(await screen.findByText(/AKIA/)).toBeInTheDocument()
  frame("safety_block", {
    message: "Loomplane's safety checks stopped this answer.",
    step_seq: 1,
  })
  expect(
    await screen.findByText("Loomplane's safety checks stopped this answer.")
  ).toBeInTheDocument()
  expect(screen.queryByText(/AKIA/)).toBeNull()
})
