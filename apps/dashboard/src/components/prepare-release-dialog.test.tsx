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

import type { Environment, Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { PrepareRelease } from "./prepare-release-dialog"

const state = vi.hoisted(() => ({
  ai: true,
  agent: true,
  navigated: [] as unknown[],
}))
vi.mock("@/lib/features", () => ({
  useFeatures: () => ({ ai: state.ai }),
  useAgent: () => ({
    enabled: state.agent,
    knowledge: false,
    loading: false,
    pending: 0,
  }),
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => (to: unknown) => {
    state.navigated.push(to)
  },
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  state.ai = true
  state.agent = true
  state.navigated = []
})

const project = { id: "proj_1", name: "Storefront" } as Project
const environments = [
  { id: "env_s", name: "staging", kind: "staging" },
  { id: "env_p", name: "production", kind: "production" },
] as Environment[]

type Write = { method: string; path: string; body: unknown }

function serve(prepare: (n: number, body: unknown) => Response) {
  const writes: Write[] = []
  const reads: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      if (method === "POST") {
        const body = JSON.parse(String(init?.body))
        writes.push({ method, path: url.pathname, body })
        return prepare(writes.length, body)
      }
      reads.push(url.pathname)
      if (url.pathname === "/api/v1/projects/proj_1/sources")
        return Response.json({
          sources: [
            { id: "s1", alias: "api" },
            { id: "s2", alias: "web" },
            { id: "s3", alias: "old", removed_at: "2026-10-01T00:00:00Z" },
          ],
        })
      return Response.json(
        { message: `unexpected ${url.pathname}` },
        { status: 404 }
      )
    }
  )
  return { writes, reads }
}

const show = (may = true) =>
  render(
    <ClientProvider client={forgeClient}>
      <PrepareRelease project={project} environments={environments} may={may} />
    </ClientProvider>
  )

const created = () =>
  Response.json(
    { id: "rrep_9", project_id: "proj_1", state: "planning" },
    { status: 202 }
  )

async function open() {
  await userEvent.click(screen.getByRole("button", { name: "Prepare release" }))
  const dialog = await screen.findByRole("dialog")
  await within(dialog).findByRole("checkbox", { name: "api" })
  return dialog
}

it("posts the chosen environments and scope", async () => {
  const dev = serve(() => created())
  show()
  const dialog = await open()
  // Removed sources aren't offered.
  expect(within(dialog).queryByRole("checkbox", { name: "old" })).toBeNull()
  expect(
    within(dialog).getByText(/always waits for a person/)
  ).toBeInTheDocument()
  expect(
    within(dialog).getByText(/still asks before production/)
  ).toBeInTheDocument()
  await userEvent.click(within(dialog).getByRole("checkbox", { name: "web" }))
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Prepare release" })
  )
  await waitFor(() =>
    expect(dev.writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/proj_1/release-reports",
        body: { from: "staging", to: "production", scope: ["api"] },
      },
    ])
  )
  await waitFor(() =>
    expect(state.navigated).toEqual([
      { to: "/release-reports/$id", params: { id: "rrep_9" } },
    ])
  )
})

it("offers to replace only after the server says a plan is open", async () => {
  const dev = serve((n) =>
    n === 1
      ? Response.json(
          {
            message:
              "production has a plan waiting for a decision: plan_5, made by Ada",
          },
          { status: 409 }
        )
      : created()
  )
  show()
  const dialog = await open()
  expect(
    within(dialog).queryByRole("checkbox", { name: "Replace that plan" })
  ).toBeNull()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Prepare release" })
  )
  expect(
    await within(dialog).findByText(/plan_5, made by Ada/)
  ).toBeInTheDocument()
  const replace = within(dialog).getByRole("checkbox", {
    name: "Replace that plan",
  })
  // It can't go ahead until the person says so.
  expect(
    within(dialog).getByRole("button", { name: "Prepare release" })
  ).toBeDisabled()
  await userEvent.click(replace)
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Prepare release" })
  )
  await waitFor(() => expect(dev.writes).toHaveLength(2))
  expect(dev.writes[0].body).not.toHaveProperty("replace")
  expect(dev.writes[1].body).toMatchObject({ replace: true })
})

it("links to the running report on an in-progress conflict", async () => {
  serve(() =>
    Response.json(
      { message: "a release is already being prepared for this environment" },
      { status: 409 }
    )
  )
  show()
  const dialog = await open()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Prepare release" })
  )
  expect(
    await within(dialog).findByText(/already being prepared/)
  ).toBeInTheDocument()
  // Never an offer to replace something that is still being prepared.
  expect(
    within(dialog).queryByRole("checkbox", { name: "Replace that plan" })
  ).toBeNull()
})

it("hides the action when the server runs no agent", () => {
  state.ai = false
  const dev = serve(() => created())
  show()
  expect(screen.queryByRole("button", { name: "Prepare release" })).toBeNull()
  expect(dev.reads).toEqual([])
})

it("hides the action from someone who may not prepare, or with the agent off", () => {
  serve(() => created())
  const view = show(false)
  expect(screen.queryByRole("button", { name: "Prepare release" })).toBeNull()
  view.unmount()
  state.agent = false
  show()
  expect(screen.queryByRole("button", { name: "Prepare release" })).toBeNull()
})
