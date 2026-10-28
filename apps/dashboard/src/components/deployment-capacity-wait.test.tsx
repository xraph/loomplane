import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { Deployment } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { DeploymentCapacityWait } from "./deployment-capacity-wait"

const permissions = vi.hoisted(() => ({ granted: new Set<string>() }))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) => permissions.granted.has(p),
  }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
    search,
    className,
  }: {
    children: React.ReactNode
    to: string
    params?: { id: string }
    search?: Record<string, string>
    className?: string
  }) => (
    <a
      href={`${to.replace("$id", params?.id ?? "")}${search ? `?${new URLSearchParams(search).toString()}` : ""}`}
      className={className}
    >
      {children}
    </a>
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.granted = new Set()
})

const staleText =
  "This changed since you opened it. Reload to see the current version."

const paused = (extra: Partial<Deployment> = {}): Deployment => ({
  id: "d",
  orgId: "org_1",
  planId: "plan_1",
  projectId: "proj_1",
  environment: "production",
  environmentId: "env_1",
  commitSha: "0123456789abcdef",
  error: "",
  retries: 0,
  sources: [],
  startedAt: "2026-10-27T10:00:00Z",
  startedBy: "user_1",
  status: "paused",
  steps: [],
  capacityWait: {
    revision: 3,
    createdAt: "2026-10-27T10:01:00Z",
    blocks: [
      {
        cloudId: "cloud_1",
        counted: 1,
        limit: 1,
        policyRevision: 2,
        poolId: "",
        reason: "hard_limit",
        requested: 1,
        resource: "gpus:nvidia-h100-80gb",
        retryable: true,
        targetId: "tgt_1",
      },
      {
        cloudId: "cloud_1",
        counted: 0,
        limit: 0,
        policyRevision: 2,
        poolId: "",
        reason: "expansion_approval",
        requested: 0,
        resource: "servers",
        retryable: true,
        targetId: "tgt_1",
      },
    ],
  },
  ...extra,
})

const wire = {
  id: "d",
  org_id: "org_1",
  plan_id: "plan_1",
  project_id: "proj_1",
  environment: "production",
  environment_id: "env_1",
  commit_sha: "0123456789abcdef",
  error: "",
  retries: 0,
  sources: [],
  started_at: "2026-10-27T10:00:00Z",
  started_by: "user_1",
  status: "running",
  steps: [],
}

type Reply = { status: number; body: unknown }

// Each POST takes the next reply; the last one repeats.
function stubFetch(resume: Reply | Reply[]) {
  const replies = Array.isArray(resume) ? resume : [resume]
  let posts = 0
  const calls: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      calls.push({
        method,
        path: url.pathname,
        body: init?.body ? JSON.parse(String(init.body)) : null,
      })
      const out =
        method === "POST"
          ? replies[Math.min(posts++, replies.length - 1)]
          : { status: 200, body: { ...wire } }
      return new Response(JSON.stringify(out.body), {
        status: out.status,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return calls
}

function show(d: Deployment) {
  return render(
    <ClientProvider client={forgeClient}>
      <DeploymentCapacityWait deployment={d} />
    </ClientProvider>
  )
}

it("explains each block and offers nothing to someone who can't approve", () => {
  stubFetch({ status: 200, body: wire })
  show(paused())
  expect(screen.getByText("Paused for capacity")).toBeInTheDocument()
  expect(
    screen.getByText(
      "NVIDIA H100 80GB GPUs is at this Cloud's limit: 1 of 1 counted, 1 more needed."
    )
  ).toBeInTheDocument()
  expect(
    screen.getByText("Waiting for a Cloud manager to approve a new server.")
  ).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Resume" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Cancel deployment" })).toBeNull()
  expect(screen.queryByRole("link", { name: "Review capacity" })).toBeNull()
})

it("says automatic provisioning is off, not that the plan changed", () => {
  stubFetch({ status: 200, body: wire })
  const d = paused()
  show({
    ...d,
    capacityWait: {
      ...d.capacityWait!,
      blocks: [
        {
          ...d.capacityWait!.blocks[1],
          reason: "review_required",
          resource: "rollout",
        },
      ],
    },
  })
  expect(
    screen.getByText(
      "Automatic provisioning is off on this Loomplane server, so no server will be added. Add or adopt one yourself."
    )
  ).toBeInTheDocument()
  expect(screen.queryByText(/plan changed/)).toBeNull()
})

it("shows nothing unless the deployment is paused with a wait", () => {
  stubFetch({ status: 200, body: wire })
  const { container, rerender } = show(paused({ status: "running" }))
  expect(container).toBeEmptyDOMElement()
  // The realtime stream sends capacity_wait: null where REST omits it.
  rerender(
    <ClientProvider client={forgeClient}>
      <DeploymentCapacityWait
        deployment={paused({
          capacityWait: null as unknown as Deployment["capacityWait"],
        })}
      />
    </ClientProvider>
  )
  expect(container).toBeEmptyDOMElement()
})

it("resumes with the wait revision", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const calls = stubFetch({ status: 200, body: wire })
  const user = userEvent.setup()
  show(paused())
  await user.click(screen.getByRole("button", { name: "Resume" }))
  await waitFor(() =>
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1)
  )
  expect(calls.filter((c) => c.method === "POST")).toEqual([
    {
      method: "POST",
      path: "/api/v1/deployments/d/capacity/resume",
      body: { expected_revision: 3 },
    },
  ])
})

it("cancels with the wait revision", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const calls = stubFetch({
    status: 200,
    body: { ...wire, status: "cancelled" },
  })
  const user = userEvent.setup()
  show(paused())
  await user.click(screen.getByRole("button", { name: "Cancel deployment" }))
  await user.click(
    await screen.findByRole("button", { name: "Cancel the deployment" })
  )
  await waitFor(() =>
    expect(calls.filter((c) => c.method === "POST")).toEqual([
      {
        method: "POST",
        path: "/api/v1/deployments/d/capacity/cancel",
        body: { expected_revision: 3 },
      },
    ])
  )
})

it("reloads after a stale resume", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const calls = stubFetch({
    status: 409,
    body: { message: "capacity wait changed" },
  })
  const user = userEvent.setup()
  show(paused())
  await user.click(screen.getByRole("button", { name: "Resume" }))
  expect(
    await screen.findByText(
      "This changed since you opened it. Reload to see the current version."
    )
  ).toBeInTheDocument()
  await waitFor(() =>
    expect(
      calls.some(
        (c) => c.method === "GET" && c.path === "/api/v1/deployments/d"
      )
    ).toBe(true)
  )
})

it("makes no Cloud capacity requests for a project-only viewer", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const calls = stubFetch({ status: 200, body: wire })
  const user = userEvent.setup()
  show(paused())
  expect(screen.queryByRole("link", { name: "Review capacity" })).toBeNull()
  await user.click(screen.getByRole("button", { name: "Resume" }))
  await waitFor(() => expect(calls.length).toBeGreaterThan(0))
  expect(
    calls.filter(
      (c) => c.path.includes("/capacity-") || c.path.includes("/expansions")
    )
  ).toEqual([])
})

it("links a Cloud manager to the Cloud's capacity", () => {
  permissions.granted = new Set([
    "deployment:approve",
    "organization_cloud:manage",
  ])
  const calls = stubFetch({ status: 200, body: wire })
  show(paused())
  expect(screen.getByRole("link", { name: "Review capacity" })).toHaveAttribute(
    "href",
    "/clouds/cloud_1?project=proj_1&tab=capacity"
  )
  expect(calls).toEqual([])
})

it("keeps the reload sentence while showing the moved wait, until the next action", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const calls = stubFetch([
    { status: 409, body: { message: "capacity wait changed" } },
    { status: 200, body: wire },
  ])
  const user = userEvent.setup()
  const { rerender } = show(paused())
  await user.click(screen.getByRole("button", { name: "Resume" }))
  expect(await screen.findByText(staleText)).toBeInTheDocument()
  const moved = paused()
  rerender(
    <ClientProvider client={forgeClient}>
      <DeploymentCapacityWait
        deployment={{
          ...moved,
          capacityWait: {
            ...moved.capacityWait!,
            revision: 4,
            blocks: [
              {
                ...moved.capacityWait!.blocks[1]!,
                reason: "provisioning",
              },
            ],
          },
        }}
      />
    </ClientProvider>
  )
  expect(screen.getByText("A new server is being created.")).toBeInTheDocument()
  expect(screen.getByText(staleText)).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Resume" }))
  await waitFor(() => expect(screen.queryByText(staleText)).toBeNull())
  expect(calls.filter((c) => c.method === "POST").map((c) => c.body)).toEqual([
    { expected_revision: 3 },
    { expected_revision: 4 },
  ])
})
