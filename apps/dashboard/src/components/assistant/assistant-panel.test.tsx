import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ShellProvider, useShell } from "@/components/shell"
import { ThemeProvider } from "@/components/theme-provider"
import { AgentProvider, FeaturesProvider } from "@/lib/features"
import { FakeEventSource } from "@/test-fixtures/event-source"

const where = vi.hoisted(() => ({ path: "/projects/proj_1" }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => () => {},
  usePath: () => where.path,
  Link: ({
    children,
    to,
    search,
    onClick,
  }: {
    children?: ReactNode
    to: string
    search?: Record<string, string>
    onClick?: () => void
  }) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} onClick={onClick}>
        {children}
      </a>
    )
  },
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))

type Write = { method: string; path: string; body: unknown }
let writes: Write[] = []

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  writes = []
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
const history = {
  conversation: { ...conversation, status: "completed" },
  more: false,
  turns: [
    {
      task_id: "atask_1",
      message: "Deploy staging?",
      status: "completed",
      error: "",
      event_seq: 2,
      created_at: "2026-11-03T10:00:00Z",
      finished_at: "2026-11-03T10:00:05Z",
      events: [
        {
          task_id: "atask_1",
          seq: 1,
          kind: "step",
          data: {},
          at: "2026-11-03T10:00:01Z",
        },
        {
          task_id: "atask_1",
          seq: 2,
          kind: "token",
          data: { text: "Staging is deploying." },
          at: "2026-11-03T10:00:02Z",
        },
      ],
    },
  ],
}

function serve() {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") writes.push({ method, path: url.pathname, body })
      if (method === "POST" && url.pathname === "/api/v1/agent/conversations")
        return Response.json({ conversation, task }, { status: 201 })
      if (url.pathname === "/api/v1/agent/conversations/conv_1")
        return Response.json(history)
      if (url.pathname === "/api/v1/agent/status")
        return Response.json({ server: true, enabled: true, knowledge: false })
      if (url.pathname === "/api/v1/agent/conversations")
        return Response.json({ conversations: [] })
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

function show() {
  serve()
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai: true }}>
        <AgentProvider>
          <ThemeProvider>
            <ShellProvider>
              <Entry />
            </ShellProvider>
          </ThemeProvider>
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
}

async function openPanel() {
  // The agent's status arrives first; until then there's nothing to open.
  await waitFor(async () => {
    await userEvent.click(screen.getByRole("button", { name: "Ask from here" }))
    expect(screen.getByRole("heading", { name: "Assistant" })).toBeVisible()
  })
}

async function send(text: string) {
  await userEvent.type(
    await screen.findByRole("textbox", { name: "Message the agent" }),
    `${text}{Enter}`
  )
  await waitFor(() => expect(FakeEventSource.instances).not.toHaveLength(0))
}

it("opens scoped to the project page it was opened from", async () => {
  show()
  await openPanel()
  expect(await screen.findByText("In api")).toBeInTheDocument()
  await send("Deploy staging?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: { message: "Deploy staging?", project_id: "proj_1" },
  })
})

it("can ask about the whole workspace instead", async () => {
  show()
  await openPanel()
  await userEvent.click(
    await screen.findByRole("button", { name: "Ask about the whole workspace" })
  )
  expect(screen.getByText("The whole workspace")).toBeInTheDocument()
  await send("What's running?")
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations",
    body: { message: "What's running?" },
  })
})

it("keeps its conversation when closed and reopened", async () => {
  show()
  await openPanel()
  await send("Deploy staging?")
  const source = FakeEventSource.latest()
  source.emit("agent.event", {
    task_id: "atask_1",
    seq: 1,
    kind: "step",
    data: {},
    at: "",
  })
  source.emit("agent.event", {
    task_id: "atask_1",
    seq: 2,
    kind: "token",
    data: { text: "Staging is deploying." },
    at: "",
  })
  source.emit("agent.end", { status: "completed" })
  expect(await screen.findByText("Staging is deploying.")).toBeInTheDocument()

  await userEvent.click(screen.getByRole("button", { name: "Close" }))
  await waitFor(() =>
    expect(screen.queryByText("Staging is deploying.")).toBeNull()
  )
  await userEvent.click(screen.getByRole("button", { name: "Ask from here" }))
  expect(await screen.findByText("Staging is deploying.")).toBeInTheDocument()
  expect(screen.getByText("Deploy staging?")).toBeInTheDocument()
  expect(screen.getByText("In api")).toBeInTheDocument()
})

it("links to the full Assistant page", async () => {
  show()
  await openPanel()
  expect(screen.queryByRole("link", { name: /Open in Assistant/ })).toBeNull()
  await send("Deploy staging?")
  expect(
    await screen.findByRole("link", { name: /Open in Assistant/ })
  ).toHaveAttribute("href", "/assistant?c=conv_1")
})
