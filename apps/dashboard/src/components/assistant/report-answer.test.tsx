import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ConversationView } from "@/components/assistant/conversation-view"

vi.mock("@/lib/router", () => ({
  useNavigate: () => () => {},
  usePath: () => "/",
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))

type Write = { method: string; path: string; body: unknown }
let writes: Write[] = []

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  writes = []
})

function history(status: string) {
  return {
    conversation: {
      id: "conv_1",
      org_id: "org",
      project_id: "proj_1",
      title: "Deploy staging?",
      started_by: "usr_1",
      status,
      last_task_id: "atask_1",
      turns: 1,
      created_at: "2026-11-03T10:00:00Z",
      updated_at: "2026-11-03T10:00:00Z",
    },
    more: false,
    turns: [
      {
        task_id: "atask_1",
        message: "Deploy staging?",
        status,
        error: "",
        event_seq: 2,
        created_at: "2026-11-03T10:00:00Z",
        finished_at: "2026-11-03T10:00:09Z",
        events: [
          {
            task_id: "atask_1",
            seq: 1,
            kind: "token",
            data: { text: "Staging is deploying." },
            at: "",
          },
          {
            task_id: "atask_1",
            seq: 2,
            kind: "status",
            data: { status },
            at: "",
          },
        ],
      },
    ],
  }
}

function show(status: string, flag?: () => Response) {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      if (method !== "GET")
        writes.push({
          method,
          path: url.pathname,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        })
      if (method === "POST" && url.pathname.endsWith("/flag"))
        return (flag ?? (() => Response.json({})))()
      if (url.pathname === "/api/v1/agent/conversations/conv_1")
        return Response.json(history(status))
      return Response.json({})
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ConversationView conversationId="conv_1" />
    </ClientProvider>
  )
}

const flag = {
  id: "aflag_1",
  org_id: "org",
  project_id: "proj_1",
  conversation_id: "conv_1",
  task_id: "atask_1",
  reported_by: "usr_1",
  reason: "inaccurate",
  note: "",
  question: "Deploy staging?",
  answer: "Staging is deploying.",
  created_at: "2026-11-03T10:00:00Z",
  updated_at: "2026-11-03T10:00:00Z",
  dismissed_by: "",
  dismissed_at: null,
}

it("offers Report on a finished answer", async () => {
  show("completed")
  expect(
    await screen.findByRole("button", { name: "Report answer" })
  ).toBeInTheDocument()
})

it("does not offer Report on an answer still running", async () => {
  show("running")
  await screen.findByText("Staging is deploying.")
  expect(screen.queryByRole("button", { name: "Report answer" })).toBeNull()
})

it("sends the reason and note, then says thanks", async () => {
  show("completed", () => Response.json(flag))
  await userEvent.click(
    await screen.findByRole("button", { name: "Report answer" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.click(within(dialog).getByLabelText("Harmful"))
  await userEvent.type(
    within(dialog).getByLabelText(/Note/),
    "It told me to drop the database"
  )
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Send report" })
  )
  expect(
    await screen.findByText(
      "Thanks. The report went to whoever runs this Loomplane."
    )
  ).toBeInTheDocument()
  expect(writes).toContainEqual({
    method: "POST",
    path: "/api/v1/agent/conversations/conv_1/turns/atask_1/flag",
    body: { reason: "harmful", note: "It told me to drop the database" },
  })
})

it("shows the server's message when the report fails", async () => {
  show("completed", () =>
    Response.json({ error: "this turn hasn't finished" }, { status: 409 })
  )
  await userEvent.click(
    await screen.findByRole("button", { name: "Report answer" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Send report" })
  )
  expect(
    await within(dialog).findByText(/turn hasn't finished/)
  ).toBeInTheDocument()
  expect(screen.queryByText(/The report went to whoever runs/)).toBeNull()
})
