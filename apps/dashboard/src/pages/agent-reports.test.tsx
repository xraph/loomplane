import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AgentReport } from "./agent-report"
import { AgentReports } from "./agent-reports"

const state = vi.hoisted(() => ({
  ai: true,
  agentOn: true,
  // The workspace's permissions; null holds every one.
  workspace: null as string[] | null,
  // Each project's permissions, by ID.
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
  useFeatures: () => ({ ai: state.ai }),
  useAgent: () => ({
    enabled: state.agentOn,
    knowledge: false,
    loading: false,
    pending: 0,
    reports: 0,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  usePath: () => "/agent/reports",
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
  state.ai = true
  state.agentOn = true
  state.workspace = null
  state.projects = {}
})

type Row = Record<string, unknown>
type Write = { method: string; path: string; body: unknown }

function report(over: Row = {}): Row {
  return {
    id: "arep_1",
    org_id: "org",
    project_id: "proj_1",
    environment_id: "env_1",
    environment: "production",
    signal: "deployment_failed",
    component: "",
    fingerprint: "fp",
    status: "open",
    severity: "high",
    title: "Deployment to production failed",
    facts: [],
    evidence: {},
    findings: null,
    repository: "acme/shop",
    occurrences: 3,
    first_seen_at: new Date(Date.now() - 3_600_000).toISOString(),
    last_seen_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    resolved_at: null,
    closed_by: "",
    resolution: "",
    taken_by: "",
    taken_at: null,
    investigation: "none",
    investigation_note: "",
    task_id: "",
    issue: { state: "none" },
    revision: 1,
    opened_by: "",
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...over,
  }
}

function serve(
  reports: Row[],
  opts: { watcher?: Row; get?: Row; close?: (body: unknown) => Response } = {}
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
      const path = url.pathname
      if (path === "/api/v1/agent/reports") return Response.json({ reports })
      if (path === "/api/v1/agent/reports/arep_1/close")
        return opts.close
          ? opts.close(body)
          : Response.json({ ...(opts.get ?? reports[0]), status: "closed" })
      if (path.startsWith("/api/v1/agent/reports/"))
        return Response.json(
          writes.length > 0
            ? { ...(opts.get ?? reports[0]), status: "closed" }
            : (opts.get ?? reports[0])
        )
      if (path === "/api/v1/agent/watcher")
        return Response.json(
          opts.watcher ?? {
            paused: false,
            issues: { configured: true, installations: [] },
          }
        )
      const perms = path.match(/^\/api\/v1\/projects\/([^/]+)\/permissions$/)
      if (perms && state.projects[perms[1]])
        return Response.json({ permissions: state.projects[perms[1]] })
      if (path === "/api/v1/projects")
        return Response.json({
          projects: [{ id: "proj_1", name: "Storefront", slug: "storefront" }],
        })
      if (path === "/api/v1/members") return Response.json({ members: [] })
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  return { writes, reads }
}

function showList() {
  return render(
    <ClientProvider client={forgeClient}>
      <AgentReports />
    </ClientProvider>
  )
}

function showOne() {
  return render(
    <ClientProvider client={forgeClient}>
      <AgentReport id="arep_1" />
    </ClientProvider>
  )
}

it("lists open reports with their issue and investigation state", async () => {
  serve([
    report({
      id: "arep_1",
      title: "Deployment to production failed",
      investigation: "done",
      issue: {
        number: 12,
        url: "https://github.com/acme/shop/issues/12",
        state: "open",
      },
    }),
    report({
      id: "arep_2",
      title: "Error rate doubled on web",
      last_seen_at: new Date(Date.now() - 60 * 60_000).toISOString(),
      occurrences: 1,
      investigation: "skipped",
      investigation_note: "the budget is spent",
      issue: { state: "failed", error: "GitHub refused the issue" },
    }),
    report({
      id: "arep_3",
      title: "Canary rolled back",
      last_seen_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
      investigation: "running",
      issue: { state: "waiting_for_permission" },
    }),
  ])
  showList()
  expect(
    await screen.findByRole("link", { name: "Deployment to production failed" })
  ).toHaveAttribute("href", "/agent/reports/arep_1")
  // Newest last seen first.
  expect(
    screen
      .getAllByRole("link", { name: /failed|doubled|rolled/ })
      .map((a) => a.textContent)
  ).toEqual([
    "Deployment to production failed",
    "Error rate doubled on web",
    "Canary rolled back",
  ])
  expect(screen.getByText("Findings ready")).toBeInTheDocument()
  expect(screen.getByText("Investigating")).toBeInTheDocument()
  expect(
    screen.getByText("Not investigated: the budget is spent")
  ).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Issue #12" })).toHaveAttribute(
    "href",
    "https://github.com/acme/shop/issues/12"
  )
  expect(
    screen.getByText("Herald only: GitHub refused the issue")
  ).toBeInTheDocument()
  expect(screen.getAllByText(/seen 3 times/)).toHaveLength(2)
  expect(screen.getByText(/seen 1 time ·/)).toBeInTheDocument()
  expect(screen.getAllByText(/Storefront · production/)).toHaveLength(3)
})

it("asks for the status that was picked", async () => {
  const { reads } = serve([report()])
  showList()
  await screen.findByText("Deployment to production failed")
  expect(reads.some((r) => r.includes("status=open"))).toBe(true)
  await userEvent.click(screen.getByRole("button", { name: "All" }))
  await waitFor(() =>
    expect(reads.some((r) => r.includes("status=all"))).toBe(true)
  )
})

it("shows the budget banner when the watcher is paused", async () => {
  serve([report()], {
    watcher: {
      paused: true,
      paused_reason: "budget",
      issues: { configured: true, installations: [] },
    },
  })
  showList()
  expect(
    await screen.findByText(
      /The agent stopped investigating: this workspace has used its AI budget for the month\. Reports still open and go out as usual\./
    )
  ).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Open AI settings" })
  ).toHaveAttribute("href", "/settings/ai")
})

it("leaves the AI settings link to people who manage AI", async () => {
  state.workspace = ["agent:use"]
  serve([report()], {
    watcher: { paused: true, issues: { configured: true, installations: [] } },
  })
  showList()
  await screen.findByText(/The agent stopped investigating/)
  expect(screen.queryByRole("link", { name: "Open AI settings" })).toBeNull()
})

it("shows nothing, and asks for nothing, with AI off", async () => {
  state.ai = false
  const { reads } = serve([report()])
  showList()
  await new Promise((r) => setTimeout(r, 30))
  expect(screen.queryByText("Deployment to production failed")).toBeNull()
  expect(reads).toEqual([])
})

it("closes a report with a reason", async () => {
  const { writes } = serve([report()])
  showOne()
  await userEvent.click(await screen.findByRole("button", { name: "Close" }))
  await userEvent.type(screen.getByLabelText("Reason"), "Fixed in v2")
  await userEvent.click(screen.getByRole("button", { name: "Close report" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/agent/reports/arep_1/close",
        body: { reason: "Fixed in v2" },
      },
    ])
  )
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
  )
  expect(screen.getByText(/^Closed/)).toBeInTheDocument()
})

it("hides close from people who can't deploy", async () => {
  state.workspace = ["agent:use"]
  state.projects = { proj_1: ["deployment:read"] }
  serve([report()])
  showOne()
  await screen.findByText("Deployment to production failed")
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
  )
})

it("hides close on a report that isn't open", async () => {
  serve([report({ status: "resolved" })])
  showOne()
  await screen.findByText("Deployment to production failed")
  expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
})

it("renders untrusted facts as text, never as HTML", async () => {
  const hostile = "<img src=x onerror=alert(1)>"
  serve([
    report({
      facts: [
        { label: "Error", value: hostile, untrusted: true },
        { label: "Deployment", value: "dep_1", path: "/deployments/dep_1" },
      ],
      evidence: { deployment_id: "dep_1", plan_id: "plan_1", watch_id: "w_1" },
      findings: {
        summary: "The build ran out of memory.",
        likely_cause: "A <b>big</b> bundle",
        suggestions: ["Raise the limit"],
        confidence: "medium",
        written_at: new Date().toISOString(),
      },
    }),
  ])
  const { container } = showOne()
  const block = await screen.findByText(hostile)
  expect(block.tagName).toBe("PRE")
  expect(block.className).toContain("font-mono")
  expect(container.querySelector("img")).toBeNull()
  expect(container.querySelector("b")).toBeNull()
  expect(screen.getByText("The build ran out of memory.")).toBeInTheDocument()
  expect(screen.getByText("Raise the limit")).toBeInTheDocument()
  // The fact and the evidence both link to the deployment.
  for (const link of screen.getAllByRole("link", { name: "dep_1" }))
    expect(link).toHaveAttribute("href", "/deployments/dep_1")
  expect(screen.getByRole("link", { name: "plan_1" })).toHaveAttribute(
    "href",
    "/deployments/plan_1"
  )
  expect(screen.getByText("w_1")).toBeInTheDocument()
})

it("says what the agent hasn't done and where the report went", async () => {
  serve([
    report({
      signal: "canary_rolled_back",
      investigation: "skipped",
      investigation_note: "the budget is spent",
      issue: { state: "no_repository" },
    }),
  ])
  showOne()
  expect(
    await screen.findByText(
      "The canary rolled traffic back to the previous revision."
    )
  ).toBeInTheDocument()
  expect(
    screen.getByText("Not investigated: the budget is spent")
  ).toBeInTheDocument()
  expect(screen.getByText(/^Herald only/)).toBeInTheDocument()
})
