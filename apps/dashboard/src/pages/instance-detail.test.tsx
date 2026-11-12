import type { ReactNode } from "react"
import { render, screen } from "@testing-library/react"
import { it, expect, vi, beforeEach } from "vitest"
import { InstanceDetail } from "./instance-detail"
const state = vi.hoisted(() => ({
  query: {
    status: "success",
    data: undefined as unknown,
    error: undefined as unknown,
    refetch: vi.fn(async () => {}),
  },
}))
vi.mock("@/client/forge/loomplane/hooks", () => ({
  useFleetInstancesGet: "instance",
}))
vi.mock("@/client/hooks", () => ({ useQuery: () => state.query }))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/page", () => ({
  Page: ({ children }: { children: ReactNode }) => <main>{children}</main>,
  PageHeading: ({
    title,
    actions,
  }: {
    title: ReactNode
    actions: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {actions}
    </header>
  ),
}))
vi.mock("@/components/fleets/instance-monitoring", () => ({
  InstanceMonitoring: ({
    projectID,
    instanceID,
  }: {
    projectID: string
    instanceID: string
  }) => (
    <div>
      Monitoring {projectID} {instanceID}
    </div>
  ),
}))
vi.mock("@/components/fleets/active-operation", () => ({
  ActiveOperation: ({
    projectID,
    instance,
    onChanged,
  }: {
    projectID: string
    instance: { id: string }
    onChanged: () => Promise<unknown>
  }) => (
    <button onClick={() => void onChanged()}>
      Operation {projectID} {instance.id}
    </button>
  ),
}))
vi.mock("@/components/fleets/instance-overview", () => ({
  InstanceOverview: () => <div>Instance details</div>,
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children }: { children: ReactNode }) => (
    <a href="/projects/project">{children}</a>
  ),
}))
beforeEach(() => {
  state.query = {
    status: "success",
    data: { id: "instance", externalRef: "sample-01" },
    error: undefined,
    refetch: vi.fn(async () => {}),
  }
})
it("mounts monitoring only inside the authorized instance view", () => {
  render(<InstanceDetail projectID="project" instanceID="instance" />)
  expect(screen.getByText("Monitoring project instance")).toBeInTheDocument()
  expect(screen.getByText("Operation project instance")).toBeInTheDocument()
})
it.each([401, 403, 404, 500])(
  "hides cached instance and monitoring after read failure %s",
  (statusCode) => {
    state.query.status = "error"
    state.query.error = { statusCode, message: "private-token" }
    render(<InstanceDetail projectID="project" instanceID="instance" />)
    expect(screen.queryByText(/Monitoring/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Operation project/)).not.toBeInTheDocument()
    expect(screen.queryByText("Instance details")).not.toBeInTheDocument()
    expect(screen.queryByText("sample-01")).not.toBeInTheDocument()
  }
)
it("keeps monitoring unmounted while the scope loads", () => {
  state.query.data = undefined
  render(<InstanceDetail projectID="project" instanceID="instance" />)
  expect(screen.getByText("Loading fleet state")).toBeInTheDocument()
  expect(screen.queryByText(/Monitoring/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Operation project/)).not.toBeInTheDocument()
})
