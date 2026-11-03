import { ClientProvider } from "@forge-go/client-react"
import {
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
import { AgentRequests } from "./agent-requests"

const state = vi.hoisted(() => ({
  runs: [] as { id: string; status: string; plan: { id: string } }[],
  agentOn: true,
  // The workspace's permissions; null holds every one.
  workspace: null as string[] | null,
  // Each project's permissions, by ID; a project left out can't be read.
  projects: {} as Record<string, string[]>,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [],
    can: (p: string) => state.workspace === null || state.workspace.includes(p),
  }),
}))
vi.mock("@/lib/features", () => ({
  useAgent: () => ({
    enabled: state.agentOn,
    knowledge: false,
    loading: false,
    pending: 0,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/approval", () => ({
  ApprovalCard: ({ run }: { run: { plan: { id: string } } }) => (
    <section aria-label="Approve this deploy">Approve {run.plan.id}</section>
  ),
}))
vi.mock("@/lib/use-runs", async (original) => ({
  ...(await original<typeof import("@/lib/use-runs")>()),
  useRuns: () => ({ runs: state.runs }),
}))
vi.mock("@/lib/router", () => ({
  usePath: () => "/agent/requests",
  useNavigate: () => () => {},
  Link: ({
    children,
    to,
    params,
  }: {
    children?: ReactNode
    to: string
    params?: Record<string, string>
  }) => (
    <a href={to.replace(/\$(\w+)/g, (_, key: string) => params?.[key] ?? "")}>
      {children}
    </a>
  ),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  state.runs = []
  state.agentOn = true
  state.workspace = null
  state.projects = {}
})

type Row = Record<string, unknown>
type Write = { method: string; path: string; body: unknown }

function request(over: Row = {}): Row {
  return {
    id: "areq_1",
    org_id: "org",
    project_id: "proj_1",
    environment_id: "",
    task_id: "atask_1",
    run_id: "arun_1",
    kind: "answer",
    status: "pending",
    question: "Which region should the cache live in?",
    context: [],
    options: [],
    target: {},
    asked_for: "usr_1",
    answered_by: "",
    answer: "",
    outcome: "",
    team_id: "",
    team_name: "",
    route_source: "admins",
    team_reason: "",
    route_note: "",
    expires_at: "2026-11-03T10:00:00Z",
    created_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    updated_at: new Date().toISOString(),
    answered_at: null,
    ...over,
  }
}

function serve(
  requests: Row[],
  extra: Record<string, (body: unknown) => Response> = {},
  // What "for you" lists, when it isn't the same as everyone's.
  mine?: Row[]
) {
  const writes: Write[] = []
  const reads: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") writes.push({ method, path: url.pathname, body })
      else reads.push(url.pathname + url.search)
      const route = extra[`${method} ${url.pathname}`]
      if (route) return route(body)
      if (url.pathname === "/api/v1/agent/requests")
        return Response.json({
          requests:
            mine && url.searchParams.get("for") === "me" ? mine : requests,
        })
      const perms = url.pathname.match(
        /^\/api\/v1\/projects\/([^/]+)\/permissions$/
      )
      if (perms && state.projects[perms[1]])
        return Response.json({ permissions: state.projects[perms[1]] })
      if (url.pathname === "/api/v1/projects")
        return Response.json({
          projects: [{ id: "proj_1", name: "Storefront", slug: "storefront" }],
        })
      if (url.pathname === "/api/v1/members")
        return Response.json({
          members: [
            { user_id: "usr_2", name: "Ada Lovelace", email: "a@x.io" },
          ],
        })
      if (url.pathname === "/api/v1/github")
        return Response.json({
          configured: true,
          install_url: "https://github.com/apps/loomplane/installations/new",
          installations: [],
        })
      return Response.json(
        { message: `unexpected ${url.pathname}` },
        { status: 404 }
      )
    }
  )
  return { writes, reads }
}

// The queries the list was asked with, whatever order they were written in.
function listed(reads: string[]) {
  return reads
    .filter((r) => r.startsWith("/api/v1/agent/requests?"))
    .map((r) => Object.fromEntries(new URL(r, "http://x").searchParams))
}

function show() {
  return render(
    <ClientProvider client={forgeClient}>
      <AgentRequests />
    </ClientProvider>
  )
}

it("renders the approval card for a plan request", async () => {
  state.runs = [{ id: "plan_1", status: "waiting", plan: { id: "plan_1" } }]
  serve([
    request({
      kind: "approve_plan",
      question: "Approve the release of storefront to production?",
      target: { plan_id: "plan_1", plan_revision: "rev_2" },
    }),
  ])
  show()
  expect(
    await screen.findByRole("region", { name: "Approve this deploy" })
  ).toHaveTextContent("Approve plan_1")
  // Declining stays on every request.
  expect(screen.getByRole("button", { name: "Decline" })).toBeInTheDocument()
})

it("links to the plan when no run is waiting on it", async () => {
  serve([
    request({
      kind: "approve_plan",
      target: { plan_id: "plan_9", plan_revision: "rev_1" },
    }),
  ])
  show()
  expect(
    await screen.findByRole("link", { name: "Open the plan" })
  ).toHaveAttribute("href", "/deployments/plan_9")
})

it("sends a secret once and clears it", async () => {
  const { writes } = serve(
    [
      request({
        kind: "provide_secret",
        question: "I need the Stripe key to finish the deploy.",
        target: { key: "STRIPE_KEY", environment: "production" },
      }),
    ],
    {
      "POST /api/v1/agent/requests/areq_1/secret": () =>
        Response.json(
          request({
            kind: "provide_secret",
            status: "answered",
            outcome: "provided",
          })
        ),
    }
  )
  show()
  const field = await screen.findByLabelText(
    "Value for STRIPE_KEY in production"
  )
  expect(field).toHaveAttribute("type", "password")
  await userEvent.type(field, "sk_live_hunter2")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/agent/requests/areq_1/secret",
        body: { value: "sk_live_hunter2" },
      },
    ])
  )
  await waitFor(() => expect(field).toHaveValue(""))
  expect(document.body.textContent).not.toContain("hunter2")
  for (const input of document.querySelectorAll("input:not([type=checkbox])"))
    expect((input as HTMLInputElement).value).toBe("")
})

it("answers with an option", async () => {
  const { writes } = serve(
    [request({ options: ["blue", "green"], question: "Which colour?" })],
    {
      "POST /api/v1/agent/requests/areq_1/answer": () =>
        Response.json(request({ status: "answered" })),
    }
  )
  show()
  await userEvent.click(await screen.findByRole("button", { name: "green" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/agent/requests/areq_1/answer",
        body: { text: "green" },
      },
    ])
  )
})

it("answers a free question with text", async () => {
  const { writes } = serve([request()], {
    "POST /api/v1/agent/requests/areq_1/answer": () =>
      Response.json(request({ status: "answered" })),
  })
  show()
  await userEvent.type(await screen.findByLabelText("Your answer"), "eu-west-1")
  await userEvent.click(screen.getByRole("button", { name: "Send" }))
  await waitFor(() => expect(writes[0]?.body).toEqual({ text: "eu-west-1" }))
})

it("finishes a connection step with Done and declines with a reason", async () => {
  const { writes } = serve(
    [request({ kind: "create_connection", target: { cloud: "aws" } })],
    {
      "POST /api/v1/agent/requests/areq_1/complete": () =>
        Response.json(request({ status: "answered" })),
      "POST /api/v1/agent/requests/areq_1/decline": () =>
        Response.json(request({ status: "declined" })),
    }
  )
  show()
  expect(
    await screen.findByRole("link", { name: "Connect aws" })
  ).toHaveAttribute("href", "/clouds")
  await userEvent.click(screen.getByRole("button", { name: "Done" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatchObject({
    path: "/api/v1/agent/requests/areq_1/complete",
  })
  await userEvent.click(screen.getByRole("button", { name: "Decline" }))
  await userEvent.type(screen.getByLabelText(/Why \(optional/), "Not this week")
  await userEvent.click(screen.getByRole("button", { name: "Decline request" }))
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[1]).toEqual({
    method: "POST",
    path: "/api/v1/agent/requests/areq_1/decline",
    body: { reason: "Not this week" },
  })
})

it("offers the GitHub install link", async () => {
  serve([
    request({ kind: "connect_github", target: { repository: "acme/shop" } }),
  ])
  show()
  expect(
    await screen.findByRole("link", { name: "Install the GitHub app" })
  ).toHaveAttribute(
    "href",
    "https://github.com/apps/loomplane/installations/new"
  )
  expect(
    screen.getByRole("button", { name: "I've connected it" })
  ).toBeInTheDocument()
})

it("says who a request was sent to and why", async () => {
  serve([
    request({
      id: "areq_1",
      question: "Is the Payments rollout window still Friday?",
      team_id: "team_1",
      team_name: "Payments",
      route_source: "named",
      team_reason: "They own the settlement job",
    }),
    request({
      id: "areq_2",
      question: "Which region?",
      team_id: "team_2",
      team_name: "Platform",
      route_source: "project",
    }),
    request({
      id: "areq_3",
      question: "Can you confirm the budget?",
      route_source: "admins",
      route_note:
        "The agent named a team that doesn't exist; sent to workspace admins",
    }),
  ])
  show()
  const named = await screen.findByRole("region", { name: /rollout window/ })
  expect(within(named).getByText("Sent to Payments")).toBeInTheDocument()
  expect(named).toHaveTextContent("They own the settlement job")
  const project = screen.getByRole("region", { name: "Which region?" })
  expect(project).toHaveTextContent("Sent to Platform")
  expect(project).toHaveTextContent("The team responsible for this project")
  const admins = screen.getByRole("region", { name: /confirm the budget/ })
  expect(admins).toHaveTextContent("Sent to the workspace admins")
  expect(admins).toHaveTextContent("The agent named a team that doesn't exist")
})

it("filters to what is asked of you, or everyone's", async () => {
  const { reads } = serve([request()])
  show()
  await screen.findByText("Which region should the cache live in?")
  expect(listed(reads)).toContainEqual({ status: "pending", for: "me" })
  const group = screen.getByRole("group", { name: "Whose requests" })
  expect(
    within(group).getByRole("button", { name: "For you" })
  ).toHaveAttribute("aria-pressed", "true")
  await userEvent.click(within(group).getByRole("button", { name: "Everyone" }))
  await waitFor(() =>
    expect(listed(reads)).toContainEqual({ status: "pending" })
  )
})

it("shows the agent as the decider", async () => {
  serve([
    request({
      kind: "approve_plan",
      status: "answered",
      outcome: "approved",
      answered_by: "agent:aorg_1",
      answered_at: new Date().toISOString(),
      question: "Approve the release?",
      target: { plan_id: "plan_1" },
    }),
    request({
      id: "areq_2",
      status: "answered",
      outcome: "answered",
      answered_by: "usr_2",
      answer: "eu-west-1",
      answered_at: new Date().toISOString(),
    }),
  ])
  show()
  await userEvent.click(
    await screen.findByRole("switch", { name: "Show settled" })
  )
  const approved = await screen.findByRole("region", {
    name: "Approve the release?",
  })
  await waitFor(() =>
    expect(approved).toHaveTextContent("Approved by Loomplane agent")
  )
  await waitFor(() =>
    expect(screen.getByText(/by Ada Lovelace/)).toBeInTheDocument()
  )
})

it("says so when the agent is off, and asks for nothing", async () => {
  state.agentOn = false
  const { reads } = serve([])
  show()
  expect(await screen.findByText("The agent is off")).toBeInTheDocument()
  expect(reads.filter((r) => r.includes("/agent/"))).toEqual([])
})

// The page shows a settle control only to someone the server lets settle
// that kind of request, and tells everyone else who can.
it("shows each control only to people who may settle it", async () => {
  state.workspace = ["agent:use", "project:read"]
  state.projects = {
    proj_1: ["project:read", "deployment:approve"],
    proj_2: ["project:read"],
  }
  const { reads } = serve([
    request({
      id: "areq_plan1",
      kind: "approve_plan",
      question: "Approve storefront?",
      target: { plan_id: "plan_1", plan_revision: "r1" },
    }),
    request({
      id: "areq_plan2",
      project_id: "proj_2",
      kind: "approve_plan",
      question: "Approve billing?",
      target: { plan_id: "plan_2", plan_revision: "r2" },
    }),
    request({
      id: "areq_secret",
      kind: "provide_secret",
      question: "I need the Stripe key.",
      target: { key: "STRIPE_KEY", environment: "production" },
    }),
    request({
      id: "areq_secret2",
      project_id: "proj_2",
      kind: "provide_secret",
      question: "I need the billing key.",
      target: { key: "BILLING_KEY", environment: "production" },
    }),
    request({
      id: "areq_gh",
      kind: "connect_github",
      question: "Connect GitHub?",
    }),
  ])
  show()
  const card = async (name: string) =>
    within(await screen.findByRole("region", { name }))

  // proj_1 lets this person approve deployments, so they may decline its
  // plan; proj_2 doesn't.
  const plan1 = await card("Approve storefront?")
  await waitFor(() =>
    expect(plan1.getByRole("button", { name: "Decline" })).toBeInTheDocument()
  )
  const plan2 = await card("Approve billing?")
  await waitFor(() =>
    expect(
      plan2.getByText(
        "Only someone who can approve deployments in this project can settle this."
      )
    ).toBeInTheDocument()
  )
  expect(plan2.queryByRole("button", { name: "Decline" })).toBeNull()

  // Neither project lets them manage variables: no secret field to type
  // into and lose.
  for (const q of ["I need the Stripe key.", "I need the billing key."]) {
    const c = await card(q)
    expect(c.queryByLabelText(/Value for/)).toBeNull()
    expect(c.queryByRole("button", { name: "Decline" })).toBeNull()
    expect(
      c.getByText(
        "Only someone who can manage this project's variables can provide it."
      )
    ).toBeInTheDocument()
  }

  // Connecting GitHub needs repository:connect on the workspace.
  const gh = await card("Connect GitHub?")
  expect(gh.queryByRole("button", { name: /connected it/ })).toBeNull()
  expect(
    gh.getByText("Only someone who can connect repositories can settle this.")
  ).toBeInTheDocument()

  // One read of permissions per project, however many rows it has.
  const permissionReads = (id: string) =>
    reads.filter((r) => r === `/api/v1/projects/${id}/permissions`).length
  expect(permissionReads("proj_1")).toBe(1)
  expect(permissionReads("proj_2")).toBe(1)
})

it("lets the asker, the routed team or an AI admin answer, and nobody else", async () => {
  state.workspace = ["agent:use", "project:read"]
  state.projects = { proj_1: ["project:read"] }
  const theirs = request({
    id: "areq_theirs",
    question: "Which region for Ada's cache?",
    asked_for: "usr_2",
    team_id: "team_ops",
    team_name: "Ops",
    route_source: "named",
  })
  const teams = request({
    id: "areq_team",
    question: "Which region for the team's cache?",
    asked_for: "usr_2",
    team_id: "team_pay",
    team_name: "Payments",
    route_source: "named",
  })
  // "For you" holds what the server routes to this person's teams.
  serve([theirs, teams], {}, [teams])
  show()
  await userEvent.click(await screen.findByRole("button", { name: "Everyone" }))
  const other = within(
    await screen.findByRole("region", { name: "Which region for Ada's cache?" })
  )
  await waitFor(() =>
    expect(
      other.getByText(
        "Only the person who asked, a member of the team it was sent to, or someone who manages the workspace's AI can answer."
      )
    ).toBeInTheDocument()
  )
  expect(other.queryByRole("textbox", { name: "Your answer" })).toBeNull()
  const mine = within(
    screen.getByRole("region", { name: "Which region for the team's cache?" })
  )
  expect(mine.getByRole("textbox", { name: "Your answer" })).toBeInTheDocument()
})
