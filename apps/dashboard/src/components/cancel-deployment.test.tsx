import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { Deployment } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { CancelDeployment } from "./cancel-deployment"

const permissions = vi.hoisted(() => ({ granted: new Set<string>() }))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) => permissions.granted.has(p),
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.granted = new Set()
})

const deployment = (
  status: string,
  extra: Partial<Deployment> = {}
): Deployment => ({
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
  startedAt: "2026-11-02T10:00:00Z",
  startedBy: "user_1",
  status,
  steps: [
    {
      actionId: "api",
      status: "pending",
      order: 0,
      name: "api",
      op: "create",
      provider: "cloudrun",
      subject: "service",
      image: "",
      message: "",
      url: "",
    },
  ],
  ...extra,
})

function stubFetch() {
  const posts: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      if (init?.method === "POST") posts.push(url.pathname)
      return new Response(
        JSON.stringify({
          id: "d",
          status: "running",
          cancel_requested_at: "2026-11-02T10:01:00Z",
          steps: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    }
  )
  return posts
}

function show(d: Deployment) {
  return render(
    <ClientProvider client={forgeClient}>
      <CancelDeployment deployment={d} />
    </ClientProvider>
  )
}

it("offers nothing to someone who can't approve, or with nothing left to stop", () => {
  stubFetch()
  const { unmount } = show(deployment("running"))
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
  unmount()
  permissions.granted = new Set(["deployment:approve"])
  const done = show(deployment("succeeded"))
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
  done.unmount()
  const last = deployment("running")
  last.steps[0].status = "deploying"
  const deploying = show(last)
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
  deploying.unmount()
  const claimed = deployment("running")
  claimed.steps[0].startedAt = "2026-11-02T10:00:30Z"
  show(claimed)
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
})

it("says a requested cancel waits for the step that's running", () => {
  permissions.granted = new Set(["deployment:approve"])
  stubFetch()
  show(deployment("running", { cancelRequestedAt: "2026-11-02T10:01:00Z" }))
  expect(
    screen.getByRole("button", { name: /Canceling after this step/ })
  ).toBeDisabled()
})

it("cancels a running deployment once confirmed", async () => {
  permissions.granted = new Set(["deployment:approve"])
  const posts = stubFetch()
  const user = userEvent.setup()
  show(deployment("running"))
  await user.click(screen.getByRole("button", { name: "Cancel" }))
  await user.click(screen.getByRole("button", { name: "Keep deploying" }))
  expect(posts).toEqual([])
  await user.click(screen.getByRole("button", { name: "Cancel" }))
  await user.click(
    await screen.findByRole("button", { name: "Cancel the deployment" })
  )
  await waitFor(() => expect(posts).toEqual(["/api/v1/deployments/d/cancel"]))
})
