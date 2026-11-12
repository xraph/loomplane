import { act, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { InstanceMonitoring } from "./instance-monitoring"

const state = vi.hoisted(() => ({
  events: {
    status: "success",
    data: undefined as unknown,
    error: undefined as unknown,
    isFetching: false,
    refetch: vi.fn(async () => {}),
  },
  metrics: {
    status: "success",
    data: undefined as unknown,
    error: undefined as unknown,
    isFetching: false,
    refetch: vi.fn(async () => {}),
  },
  args: { events: undefined as unknown, metrics: undefined as unknown },
}))
vi.mock("@/client/forge/loomplane/hooks", () => ({
  useFleetEventsList: "events",
  useFleetMetricsGet: "metrics",
}))
vi.mock("@/client/hooks", () => ({
  useQuery: (name: "events" | "metrics", args: unknown) => {
    state.args[name] = args
    return state[name]
  },
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => (
    <a href="/projects/project">{children}</a>
  ),
}))

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date("2026-11-01T12:00:00Z"))
  state.events = {
    status: "success",
    data: {
      items: [
        {
          id: "event",
          sequence: 1,
          generation: 1,
          kind: "lifecycle",
          occurredAt: "2026-11-01T11:55:00Z",
          lifecycle: { state: "ready" },
        },
      ],
      replayTruncated: false,
      nextCursor: "page-2",
    },
    error: undefined,
    isFetching: false,
    refetch: vi.fn(async () => {}),
  }
  state.metrics = {
    status: "success",
    data: {
      cpuUtilization: 0.5,
      partial: false,
      freshness: "fresh",
      coverage: { expected: 1, observed: 1, fresh: 1, cpu: 1, replicas: 1 },
    },
    error: undefined,
    isFetching: false,
    refetch: vi.fn(async () => {}),
  }
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  })
})
afterEach(() => {
  vi.useRealTimers()
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  })
})
it("uses project and instance scope with a bounded page and the server default metric window", () => {
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  expect(state.args.events).toEqual({
    path: { project_id: "project", id: "instance" },
    query: { after: "", limit: 50 },
  })
  expect(state.args.metrics).toEqual({
    path: { project_id: "project", id: "instance" },
  })
  expect(screen.getByLabelText("CPU utilization")).toHaveTextContent("50%")
})
it("forwards only the next cursor and offers a first-page reset", () => {
  const view = render(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  fireEvent.click(screen.getByRole("button", { name: "Next events" }))
  expect(state.args.events).toMatchObject({
    query: { after: "page-2", limit: 50 },
  })
  state.events.data = { items: [], replayTruncated: false }
  view.rerender(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  expect(screen.queryByText("Ready")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "First event page" }))
  expect(state.args.events).toMatchObject({ query: { after: "", limit: 50 } })
})
it.each([401, 403, 404, 500])(
  "hides cached monitoring values after status %s",
  (statusCode) => {
    state.metrics.status = state.events.status = "error"
    state.metrics.error = state.events.error = {
      statusCode,
      message: "private-token",
    }
    render(<InstanceMonitoring projectID="project" instanceID="instance" />)
    expect(screen.queryByText("50%")).not.toBeInTheDocument()
    expect(screen.queryByText("Ready")).not.toBeInTheDocument()
    expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
  }
)
it("refetches the server window on visible idle intervals without changing the cache key", async () => {
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  await act(async () => {
    vi.advanceTimersByTime(15000)
  })
  expect(state.args.metrics).toEqual({
    path: { project_id: "project", id: "instance" },
  })
  expect(state.metrics.refetch).toHaveBeenCalledOnce()
  expect(state.events.refetch).toHaveBeenCalledOnce()
})
it("does not refetch while a read is pending or the document is hidden", async () => {
  state.metrics.isFetching = true
  const view = render(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  const original = state.args.metrics
  await act(async () => {
    vi.advanceTimersByTime(30000)
  })
  expect(state.args.metrics).toEqual(original)
  expect(state.metrics.refetch).not.toHaveBeenCalled()
  state.metrics.isFetching = false
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "hidden",
  })
  view.rerender(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  const calls = state.events.refetch.mock.calls.length
  await act(async () => {
    vi.advanceTimersByTime(30000)
  })
  expect(state.args.metrics).toEqual(original)
  expect(state.events.refetch).toHaveBeenCalledTimes(calls)
  expect(state.metrics.refetch).not.toHaveBeenCalled()
})
it.each([401, 403, 404])(
  "stops polling after status %s",
  async (statusCode) => {
    state.metrics.status = state.events.status = "error"
    state.metrics.error = state.events.error = { statusCode }
    render(<InstanceMonitoring projectID="project" instanceID="instance" />)
    const original = state.args.metrics
    await act(async () => {
      vi.advanceTimersByTime(60000)
    })
    expect(state.args.metrics).toEqual(original)
    expect(state.metrics.refetch).not.toHaveBeenCalled()
    expect(state.events.refetch).not.toHaveBeenCalled()
  }
)
it("manual metric refresh uses the current query even when the clock advances", async () => {
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  vi.setSystemTime(new Date("2026-11-01T12:01:00Z"))
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh observations" })
    )
  })
  expect(state.args.metrics).toEqual({
    path: { project_id: "project", id: "instance" },
  })
  expect(state.metrics.refetch).toHaveBeenCalledOnce()
})
it("rechecks an unchanged replay cursor without jumping to the first page", async () => {
  const view = render(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  fireEvent.click(screen.getByRole("button", { name: "Next events" }))
  state.events.data = {
    items: [],
    replayTruncated: false,
    nextCursor: "page-2",
  }
  view.rerender(
    <InstanceMonitoring projectID="project" instanceID="instance" />
  )
  expect(screen.getByText("No later events")).toBeInTheDocument()
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Check for later events" })
    )
  })
  expect(state.events.refetch).toHaveBeenCalledOnce()
  expect(state.args.events).toMatchObject({ query: { after: "page-2" } })
})

it("sends no client timestamps when the browser clock is years ahead", () => {
  vi.setSystemTime(new Date("2036-11-01T12:00:00Z"))
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  expect(state.args.metrics).toEqual({
    path: { project_id: "project", id: "instance" },
  })
})
it("does not overlap a pending polling request", async () => {
  let finish = () => {}
  state.metrics.refetch.mockImplementation(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve
      })
  )
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  await act(async () => {
    vi.advanceTimersByTime(15000)
  })
  expect(state.metrics.refetch).toHaveBeenCalledOnce()
  await act(async () => {
    vi.advanceTimersByTime(45000)
  })
  expect(state.metrics.refetch).toHaveBeenCalledOnce()
  await act(async () => {
    finish()
  })
  await act(async () => {
    vi.advanceTimersByTime(15000)
  })
  expect(state.metrics.refetch).toHaveBeenCalledTimes(2)
  await act(async () => {
    finish()
  })
})
it("retries a failed metric read with the server-relative query", async () => {
  state.metrics.status = "error"
  state.metrics.error = { statusCode: 500 }
  render(<InstanceMonitoring projectID="project" instanceID="instance" />)
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  })
  expect(state.metrics.refetch).toHaveBeenCalledOnce()
  expect(state.args.metrics).toEqual({
    path: { project_id: "project", id: "instance" },
  })
})
