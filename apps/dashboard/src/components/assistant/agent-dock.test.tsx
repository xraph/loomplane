import { ClientProvider } from "@forge-go/client-react"
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AgentDock } from "@/components/assistant/agent-dock"
import { ShellProvider, useShell } from "@/components/shell"
import { ThemeProvider } from "@/components/theme-provider"
import { AgentProvider, FeaturesProvider } from "@/lib/features"
import { FakeEventSource } from "@/test-fixtures/event-source"

const BUSY =
  "The agent is still on your last message. Wait for it to finish, answer what it asked, or stop it."

// The page's path, as the router would hand it out: a change re-renders
// whoever reads it.
const where = vi.hoisted(() => ({
  path: "/projects/proj_1",
  listeners: new Set<() => void>(),
}))
function goTo(path: string) {
  act(() => {
    where.path = path
    where.listeners.forEach((fn) => fn())
  })
}
vi.mock("@/lib/router", async () => {
  const { useSyncExternalStore } = await import("react")
  return {
    useNavigate: () => () => {},
    useSearchParams: () => ({}),
    usePath: () =>
      useSyncExternalStore(
        (fn: () => void) => {
          where.listeners.add(fn)
          return () => where.listeners.delete(fn)
        },
        () => where.path
      ),
    Link: ({
      children,
      to,
      search,
    }: {
      children?: ReactNode
      to: string
      search?: Record<string, string>
    }) => {
      const query = new URLSearchParams(search ?? {}).toString()
      return <a href={query ? `${to}?${query}` : to}>{children}</a>
    },
  }
})
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))

type Write = { method: string; path: string; body: unknown }
type Route = (body: unknown) => Response
let writes: Write[] = []
let reads: string[] = []

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  writes = []
  reads = []
  where.path = "/projects/proj_1"
})

const conversation = {
  id: "conv_1",
  org_id: "org",
  project_id: "proj_1",
  title: "Deploy staging?",
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
  project_id: "proj_1",
  kind: "chat",
  session_id: "conv_1",
  status: "queued",
  requested_by: "usr_1",
  event_seq: 0,
  error: "",
  metadata: {},
  created_at: "2026-11-03T10:00:00Z",
  updated_at: "2026-11-03T10:00:00Z",
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

// The turn as the server keeps it once it's over.
function storedHistory(
  status: string,
  text: string,
  message = "Deploy staging"
) {
  return {
    conversation: { ...conversation, status },
    more: false,
    turns: [
      {
        task_id: "atask_1",
        message,
        status,
        error: "",
        event_seq: 3,
        created_at: "2026-11-03T10:00:00Z",
        finished_at: "2026-11-03T10:00:09Z",
        events: [
          { task_id: "atask_1", seq: 1, kind: "step", data: {}, at: "" },
          { task_id: "atask_1", seq: 2, kind: "token", data: { text }, at: "" },
          {
            task_id: "atask_1",
            seq: 3,
            kind: "status",
            data: { status },
            at: "",
          },
        ],
      },
    ],
  }
}

const started: Record<string, Route> = {
  "POST /api/v1/agent/conversations": () =>
    Response.json({ conversation, task }, { status: 201 }),
}

function serve(
  routes: Record<string, Route> = {},
  conversations: unknown[] = []
) {
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
        return Response.json({ server: true, enabled: true, knowledge: false })
      if (url.pathname === "/api/v1/agent/conversations")
        return Response.json({ conversations })
      const one = /^\/api\/v1\/agent\/conversations\/([^/]+)$/.exec(
        url.pathname
      )
      if (one && (method === "PATCH" || method === "DELETE")) {
        const i = conversations.findIndex(
          (c) => (c as { id: string }).id === one[1]
        )
        if (method === "DELETE") {
          conversations.splice(i, 1)
          return new Response(null, { status: 204 })
        }
        conversations[i] = { ...(conversations[i] as object), ...body }
        return Response.json(conversations[i])
      }
      if (url.pathname === "/api/v1/agent/conversations/conv_1")
        return Response.json(
          storedHistory("completed", "Staging is deploying.", "Deploy staging?")
        )
      if (url.pathname === "/api/v1/agent/requests")
        return Response.json({ requests: [] })
      return Response.json({
        projects: [
          {
            id: "proj_1",
            name: "api",
            slug: "api",
            description: "",
            repositories: [],
          },
          {
            id: "proj_2",
            name: "web",
            slug: "web",
            description: "",
            repositories: [],
          },
        ],
        members: [],
        installations: [],
      })
    }
  )
}

function Entry() {
  const { openAssistant } = useShell()
  return <button onClick={() => openAssistant()}>Ask from here</button>
}

function show(
  routes: Record<string, Route> = {},
  conversations: unknown[] = []
) {
  serve(routes, conversations)
  return mount()
}

function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai: true }}>
        <AgentProvider>
          <ThemeProvider>
            <ShellProvider>
              <Entry />
              <AgentDock />
            </ShellProvider>
          </ThemeProvider>
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
}

const panel = () => screen.getByRole("complementary", { name: "Agent" })

async function openPanel() {
  // The agent's status arrives first; until then there's nothing to open.
  await waitFor(async () => {
    await userEvent.click(screen.getByRole("button", { name: "Ask from here" }))
    expect(panel()).toBeVisible()
  })
}

async function send(text: string) {
  await userEvent.type(
    await screen.findByRole("textbox", { name: "Message the agent" }),
    `${text}{Enter}`
  )
  await waitFor(() => expect(FakeEventSource.instances).not.toHaveLength(0))
  seq = 0
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

async function close() {
  await userEvent.click(screen.getByRole("button", { name: "Close the agent" }))
  await waitFor(() =>
    expect(screen.queryByRole("complementary", { name: "Agent" })).toBeNull()
  )
}

const scope = () =>
  screen.getByRole("combobox", { name: "What the conversation is about" })

// Where it sits, and what it keeps.

it("opens docked, scoped to the project page it was opened from", async () => {
  show(started)
  await openPanel()
  expect(panel()).toHaveAttribute("data-placement", "docked")
  await waitFor(() => expect(scope()).toHaveTextContent("In api"))
  expect(screen.getByText("What can I help with?")).toBeInTheDocument()
  await send("Deploy staging?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: {
      message: "Deploy staging?",
      project_id: "proj_1",
      // What you're looking at goes with it.
      context: {
        kind: "project",
        label: "Project api",
        path: "/projects/proj_1",
        project_id: "proj_1",
      },
    },
  })
})

it("follows the page to the whole workspace while no conversation is open", async () => {
  show(started)
  await openPanel()
  await waitFor(() => expect(scope()).toHaveTextContent("In api"))
  goTo("/deployments")
  await waitFor(() => expect(scope()).toHaveTextContent("The whole workspace"))
  await send("What's running?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: {
      message: "What's running?",
      context: { kind: "page", label: "Deployments", path: "/deployments" },
    },
  })
})

it("asks one of its suggestions with a click", async () => {
  show(started)
  await openPanel()
  await userEvent.click(
    await screen.findByRole("button", { name: /How is api doing right now\?/ })
  )
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "POST",
      path: "/api/v1/agent/conversations",
      body: {
        message: "How is api doing right now?",
        project_id: "proj_1",
        context: {
          kind: "project",
          label: "Project api",
          path: "/projects/proj_1",
          project_id: "proj_1",
        },
      },
    })
  )
})

it("keeps its conversation streaming while closed", async () => {
  show(started, [{ ...conversation, status: "completed" }])
  await openPanel()
  await send("Deploy staging?")
  frame("step")
  frame("token", { text: "Staging is " })
  await close()
  // Hidden, not gone: the answer keeps coming in.
  frame("token", { text: "deploying." })
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  await userEvent.click(screen.getByRole("button", { name: "Ask from here" }))
  expect(
    await within(panel()).findByText("Staging is deploying.")
  ).toBeInTheDocument()
  expect(
    within(panel()).getAllByText("Deploy staging?").length
  ).toBeGreaterThan(0)
  expect(screen.getByTitle(/stays about what its first/)).toHaveTextContent(
    "api"
  )
})

it("keeps an open conversation's project on another project's page", async () => {
  show(started, [{ ...conversation, status: "completed" }])
  await openPanel()
  await send("Deploy staging?")
  await close()

  goTo("/projects/proj_2")
  await userEvent.click(screen.getByRole("button", { name: "Ask from here" }))
  expect(
    await screen.findByTitle(/stays about what its first/)
  ).toHaveTextContent("api")
  // A page from another project isn't offered with it.
  expect(screen.queryByText("Project web")).toBeNull()

  // A new conversation is about the page you're on.
  await userEvent.click(
    screen.getByRole("button", { name: "New conversation" })
  )
  await waitFor(() => expect(scope()).toHaveTextContent("In web"))
})

it("comes back after a reload where you left it", async () => {
  show(started, [{ ...conversation, status: "completed" }])
  await openPanel()
  await send("Deploy staging?")
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  await userEvent.click(
    screen.getByRole("button", { name: "Where the agent sits" })
  )
  await userEvent.click(
    await screen.findByRole("menuitem", { name: /Float over the page/ })
  )
  expect(panel()).toHaveAttribute("data-placement", "floating")

  cleanup()
  forgeClient.clear()
  FakeEventSource.reset()
  mount()
  // Open, floating, on the same conversation, read back from history.
  const again = await screen.findByRole(
    "complementary",
    { name: "Agent" },
    { timeout: 5_000 }
  )
  expect(again).toHaveAttribute("data-placement", "floating")
  expect(
    await within(again).findByText("Staging is deploying.")
  ).toBeInTheDocument()
  expect(reads).toContain("/api/v1/agent/conversations/conv_1")
})

it("goes full width and back", async () => {
  show()
  await openPanel()
  await userEvent.click(screen.getByRole("button", { name: "Full width" }))
  expect(panel()).toHaveAttribute("data-placement", "focus")
  // At full width, your conversations sit beside the chat.
  expect(
    screen.getByRole("navigation", { name: "Conversations" })
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Leave full width" })
  )
  expect(panel()).toHaveAttribute("data-placement", "docked")
  expect(screen.queryByRole("navigation", { name: "Conversations" })).toBeNull()
})

it("resizes from its edge and keeps the width", async () => {
  show()
  await openPanel()
  const edge = screen.getByRole("separator", {
    name: "Resize the agent's panel",
  })
  const before = Number(edge.getAttribute("aria-valuenow"))
  edge.focus()
  await userEvent.keyboard("{ArrowLeft}{ArrowLeft}")
  await waitFor(() =>
    expect(Number(edge.getAttribute("aria-valuenow"))).toBe(before + 32)
  )
  expect(
    JSON.parse(localStorage.getItem("loomplane.agent.dock") ?? "{}").width
  ).toBe(before + 32)
})

// The history.

const listed = [
  {
    ...conversation,
    id: "conv_a",
    title: "Why is api slow?",
    project_id: "proj_1",
    status: "completed",
    updated_at: "2026-11-03T09:00:00Z",
  },
  {
    ...conversation,
    id: "conv_b",
    title: "Release web",
    project_id: "proj_2",
    status: "waiting",
    updated_at: "2026-11-03T11:00:00Z",
  },
  {
    ...conversation,
    id: "conv_c",
    title: "What's deployed?",
    project_id: "",
    status: "failed",
    updated_at: "2026-11-03T08:00:00Z",
  },
]

async function openHistory() {
  await userEvent.click(
    screen.getByRole("button", { name: "Show conversations" })
  )
  return screen.findByRole("navigation", { name: "Conversations" })
}

const sections = (nav: HTMLElement) =>
  within(nav)
    .queryAllByRole("region")
    .map((s) => s.getAttribute("aria-label"))

it("groups conversations by project, filters and pins them", async () => {
  show(
    {},
    listed.map((c) => ({ ...c }))
  )
  await openPanel()
  const nav = await openHistory()
  await waitFor(() =>
    expect(sections(nav)).toEqual(["web", "api", "Whole workspace"])
  )

  await userEvent.click(within(nav).getByRole("radio", { name: "Needs you" }))
  expect(sections(nav)).toEqual(["web"])
  await userEvent.click(within(nav).getByRole("radio", { name: "All" }))

  await userEvent.type(
    within(nav).getByRole("textbox", { name: "Search conversations" }),
    "slow"
  )
  expect(within(nav).getByText("Why is api slow?")).toBeInTheDocument()
  expect(within(nav).queryByText("Release web")).toBeNull()
  await userEvent.clear(
    within(nav).getByRole("textbox", { name: "Search conversations" })
  )

  await userEvent.click(
    within(nav).getByRole("button", { name: "Pin What's deployed?" })
  )
  // Pins are the server's, so they follow you to every browser.
  expect(writes).toContainEqual({
    method: "PATCH",
    path: "/api/v1/agent/conversations/conv_c",
    body: { pinned: true },
  })
  await waitFor(() => expect(sections(nav)).toEqual(["Pinned", "web", "api"]))

  await userEvent.click(
    within(nav).getByRole("button", { name: "Group by date" })
  )
  expect(sections(nav)[0]).toBe("Pinned")
})

it("renames and deletes a conversation from its menu", async () => {
  show(
    {},
    listed.map((c) => ({ ...c }))
  )
  await openPanel()
  const nav = await openHistory()
  await userEvent.click(
    await within(nav).findByRole("button", { name: "More for Release web" })
  )
  await userEvent.click(await screen.findByRole("menuitem", { name: "Rename" }))
  const name = within(nav).getByRole("textbox", { name: "Conversation name" })
  await userEvent.clear(name)
  await userEvent.type(name, "Ship web{Enter}")
  expect(writes).toContainEqual({
    method: "PATCH",
    path: "/api/v1/agent/conversations/conv_b",
    body: { title: "Ship web" },
  })
  expect(await within(nav).findByText("Ship web")).toBeInTheDocument()

  await userEvent.click(
    within(nav).getByRole("button", { name: "More for Ship web" })
  )
  await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }))
  await userEvent.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", {
      name: "Delete",
    })
  )
  expect(writes).toContainEqual({
    method: "DELETE",
    path: "/api/v1/agent/conversations/conv_b",
    body: undefined,
  })
  await waitFor(() => expect(within(nav).queryByText("Ship web")).toBeNull())
})

it("leaves the page out of a message when you take it off", async () => {
  show(started)
  await openPanel()
  await userEvent.click(
    await screen.findByRole("button", { name: "Don't send Project api" })
  )
  expect(
    screen.getByRole("button", { name: /Project api/ })
  ).toBeInTheDocument()
  await send("Deploy staging?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: { message: "Deploy staging?", project_id: "proj_1" },
  })
})

it("opens a conversation from the history", async () => {
  show(
    {
      "GET /api/v1/agent/conversations/conv_c": () =>
        Response.json(
          storedHistory("failed", "No access.", "What's deployed?")
        ),
    },
    listed
  )
  await openPanel()
  const nav = await openHistory()
  await userEvent.click(await within(nav).findByText("What's deployed?"))
  // The history closes over a narrow panel, and the conversation opens.
  await waitFor(() =>
    expect(
      screen.queryByRole("navigation", { name: "Conversations" })
    ).toBeNull()
  )
  expect(
    within(panel()).getByRole("heading", { name: "What's deployed?" })
  ).toBeInTheDocument()
  expect(reads).toContain("/api/v1/agent/conversations/conv_c")
})

// What a conversation shows.

it("streams the answer, code included", async () => {
  show(started)
  await openPanel()
  await send("What's deployed?")
  expect(FakeEventSource.latest().url).toMatch(
    /\/api\/v1\/agent\/tasks\/atask_1\/stream\?after=0$/
  )
  frame("run_started")
  frame("step")
  frame("token", { text: "Two **projects**.\n\n```yaml\nreplicas: 2\n```\n" })
  expect(await screen.findByText("projects")).toHaveProperty(
    "tagName",
    "STRONG"
  )
  expect(screen.getByText("yaml")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Copy code" })).toBeInTheDocument()
  expect(screen.getByText(/replicas: 2/)).toBeInTheDocument()
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  expect(
    await screen.findByRole("button", { name: "Copy answer" })
  ).toBeInTheDocument()
})

it("shows a tool step with its risk", async () => {
  show(started)
  await openPanel()
  await send("Ship the release")
  frame("step")
  frame("tool_call", {
    tool: "approve_plan",
    call_id: "call_1",
    risk: "promote_production",
    arguments: { plan_id: "plan_9" },
  })
  // A quiet line: what it's doing, and its risk when that's more than a read.
  const row = await screen.findByRole("button", { name: /Approving plan/ })
  expect(within(row).getByText("Production release")).toBeInTheDocument()
  expect(screen.queryByText(/plan_9/)).toBeNull()
  await userEvent.click(row)
  expect(await screen.findByText(/"plan_id": "plan_9"/)).toBeInTheDocument()
  expect(screen.getByText("approve_plan")).toBeInTheDocument()
  frame("tool_result", {
    tool: "approve_plan",
    call_id: "call_1",
    outcome: "ok",
  })
  expect(
    await screen.findByRole("button", { name: /Approved plan/ })
  ).toBeInTheDocument()
})

it("folds tool steps taken back to back into one line", async () => {
  show(started)
  await openPanel()
  await send("What's broken?")
  frame("step")
  frame("tool_call", { tool: "list_deployments", call_id: "c1", risk: "read" })
  frame("tool_result", {
    tool: "list_deployments",
    call_id: "c1",
    outcome: "ok",
  })
  frame("tool_call", { tool: "read_logs", call_id: "c2", risk: "read" })
  frame("tool_result", {
    tool: "read_logs",
    call_id: "c2",
    outcome: "failed",
    error: "gone",
  })
  const run = await screen.findByRole("button", {
    name: /Listed deployments, read logs/,
  })
  expect(run).toHaveAttribute("aria-expanded", "true")
  expect(within(run).getByText("1 failed")).toBeInTheDocument()
  frame("step")
  frame("token", { text: "The logs are gone." })
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  // Once the answer moves on, the steps fold away until you open them.
  await screen.findByText("The logs are gone.")
  await waitFor(() => expect(run).toHaveAttribute("aria-expanded", "false"))
  await userEvent.click(run)
  expect(run).toHaveAttribute("aria-expanded", "true")
  expect(
    await screen.findAllByRole("button", { name: /read logs/i })
  ).toHaveLength(2)
})

it("answers the agent's question inline", async () => {
  show({
    ...started,
    "GET /api/v1/agent/requests/areq_1": () => Response.json(request()),
    "POST /api/v1/agent/requests/areq_1/answer": () =>
      Response.json(request({ status: "answered", answer: "eu-west-1" })),
  })
  await openPanel()
  await send("Set up a cache")
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
  show({
    ...started,
    "POST /api/v1/agent/tasks/atask_1/cancel": () =>
      Response.json({ ...task, status: "cancelled" }),
  })
  await openPanel()
  await send("Deploy staging")
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
  // Up brings the last message back.
  await userEvent.type(box, "{ArrowUp}")
  expect(box).toHaveValue("Deploy staging")
})

it("shows why a message was refused", async () => {
  show({
    "POST /api/v1/agent/conversations": () =>
      Response.json({ message: BUSY }, { status: 409 }),
  })
  await openPanel()
  const box = await screen.findByRole("textbox", { name: "Message the agent" })
  await userEvent.type(box, "Deploy staging{Enter}")
  expect(await screen.findByRole("alert")).toHaveTextContent(BUSY)
  // The message goes back in the box, and off the page.
  expect(
    screen.getByRole("textbox", { name: "Message the agent" })
  ).toHaveValue("Deploy staging")
  expect(screen.getByText("What can I help with?")).toBeInTheDocument()
  expect(FakeEventSource.instances).toHaveLength(0)
})

it("renders a blocked answer as withheld", async () => {
  show(started)
  await openPanel()
  await send("Show me the AWS key")
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

it("finishes a turn from history when its stream is lost after the end", async () => {
  show({
    ...started,
    "GET /api/v1/agent/tasks/atask_1": () =>
      Response.json({ ...task, status: "completed" }),
    "GET /api/v1/agent/conversations/conv_1": () =>
      Response.json(storedHistory("completed", "Staging is on v1.5.")),
  })
  await openPanel()
  await send("Deploy staging")
  frame("step")
  frame("token", { text: "Staging is" })
  expect(await screen.findByText("Staging is")).toBeInTheDocument()
  // The browser gives up: the server refused the reconnect.
  FakeEventSource.latest().fail(true)
  expect(await screen.findByText("Staging is on v1.5.")).toBeInTheDocument()
  const box = screen.getByRole("textbox", { name: "Message the agent" })
  await waitFor(() => expect(box).toBeEnabled())
  expect(screen.queryByText(/Lost the connection/)).toBeNull()
  expect(reads).toContain("/api/v1/agent/tasks/atask_1")
})

it("offers to pick a lost stream up again while the agent works", async () => {
  show({
    ...started,
    "GET /api/v1/agent/tasks/atask_1": () =>
      Response.json({ ...task, status: "running" }),
  })
  await openPanel()
  await send("Deploy staging")
  frame("step")
  frame("token", { text: "Deploying " })
  expect(await screen.findByText("Deploying")).toBeInTheDocument()
  FakeEventSource.latest().fail(true)
  expect(
    await screen.findByText(/Lost the connection to the agent\./)
  ).toBeInTheDocument()
  expect(
    screen.getByRole("textbox", { name: "Message the agent" })
  ).toBeDisabled()

  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(2))
  // It picks up after the last event the page has.
  expect(FakeEventSource.latest().url).toMatch(/\/atask_1\/stream\?after=2$/)
  expect(screen.queryByText(/Lost the connection/)).toBeNull()
  frame("token", { text: "staging now." })
  FakeEventSource.latest().emit("agent.end", { status: "completed" })
  expect(await screen.findByText("Deploying staging now.")).toBeInTheDocument()
})

it("stops a turn whose stream was lost", async () => {
  let status = "running"
  show({
    ...started,
    "GET /api/v1/agent/tasks/atask_1": () => Response.json({ ...task, status }),
    "POST /api/v1/agent/tasks/atask_1/cancel": () => {
      status = "cancelled"
      return Response.json({ ...task, status })
    },
    "GET /api/v1/agent/conversations/conv_1": () =>
      Response.json(storedHistory("cancelled", "Deploying")),
  })
  await openPanel()
  await send("Deploy staging")
  frame("step")
  frame("token", { text: "Deploying" })
  FakeEventSource.latest().fail(true)
  expect(await screen.findByText(/Lost the connection/)).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Stop" }))
  expect(await screen.findByText("Stopped.")).toBeInTheDocument()
  await waitFor(() =>
    expect(
      screen.getByRole("textbox", { name: "Message the agent" })
    ).toBeEnabled()
  )
  expect(screen.queryByText(/Lost the connection/)).toBeNull()
})
