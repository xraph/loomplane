import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ReleaseReportsCard } from "./release-reports-card"

const state = vi.hoisted(() => ({ ai: true, agent: true }))
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
  state.ai = true
  state.agent = true
})

function serve(reports: unknown[] | "agent off") {
  const reads: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    reads.push(url.pathname + url.search)
    if (url.pathname !== "/api/v1/projects/proj_1/release-reports")
      return Response.json({ message: "unexpected" }, { status: 404 })
    // What the server answers while the workspace's agent is off: the
    // scope binder's 409, never an empty list.
    if (reports === "agent off")
      return Response.json(
        {
          message:
            "the agent is off for this workspace; an admin can turn it on in AI settings",
        },
        { status: 409 }
      )
    return Response.json({ reports })
  })
  return { reads }
}

const show = () =>
  render(
    <ClientProvider client={forgeClient}>
      <ReleaseReportsCard projectId="proj_1" />
    </ClientProvider>
  )

it("lists the latest five with their state", async () => {
  const dev = serve([
    {
      id: "rrep_1",
      from_environment: "staging",
      to_environment: "production",
      state: "waiting_for_person",
      created_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    },
  ])
  show()
  const card = await screen.findByRole("region", { name: "Release reports" })
  expect(
    await within(card).findByText("Waiting for a person")
  ).toBeInTheDocument()
  expect(within(card).getByRole("link")).toHaveAttribute(
    "href",
    "/release-reports/rrep_1"
  )
  expect(dev.reads[0]).toContain("limit=5")
})

it("renders nothing and asks for nothing when the server runs no agent", () => {
  state.ai = false
  const dev = serve([])
  const { container } = show()
  expect(container).toBeEmptyDOMElement()
  expect(dev.reads).toEqual([])
})

it("renders nothing and asks for nothing while the workspace's agent is off", async () => {
  state.agent = false
  const dev = serve("agent off")
  const { container } = show()
  // Give a fetch, had one started, time to answer.
  await new Promise((r) => setTimeout(r, 50))
  expect(container).toBeEmptyDOMElement()
  expect(dev.reads).toEqual([])
  expect(screen.queryByRole("alert")).toBeNull()
  expect(screen.queryByText(/couldn't be loaded/)).toBeNull()
})

it("shows no error when the agent turns off while the page is open", async () => {
  // The status still says on; the server already says off.
  const dev = serve("agent off")
  const { container } = show()
  await waitFor(() => expect(dev.reads).toHaveLength(1))
  await new Promise((r) => setTimeout(r, 50))
  expect(container).toBeEmptyDOMElement()
  expect(screen.queryByRole("alert")).toBeNull()
})
