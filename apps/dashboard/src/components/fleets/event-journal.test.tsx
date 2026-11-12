import { fireEvent, render, screen, within } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { EventJournal } from "./event-journal"

vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    params,
    search,
  }: {
    children: React.ReactNode
    params: { id: string }
    search: { project: string; tab: string }
  }) => (
    <a
      href={`/clouds/${params.id}?project=${search.project}&tab=${search.tab}`}
    >
      {children}
    </a>
  ),
}))
const item = {
  id: "event-1",
  sequence: 1,
  kind: "lifecycle",
  generation: 2,
  occurredAt: "2026-11-01T12:00:00Z",
  operationId: "operation-1",
  lifecycle: { state: "waiting_approval", reason: "approval_required" },
}
const retry = async () => {}

it("keeps loading separate from an empty event journal", () => {
  render(<EventJournal projectID="project" loading retry={retry} />)
  expect(screen.getByRole("status")).toHaveTextContent("Loading events")
  expect(screen.queryByText("No events yet")).not.toBeInTheDocument()
})
it("uses the shared illustrated empty state with a working refresh", () => {
  const refresh = vi.fn(async () => {})
  render(
    <EventJournal
      projectID="project"
      page={{ items: [], replayTruncated: false }}
      retry={refresh}
    />
  )
  expect(
    screen.getByText("No events yet").closest('[data-slot="empty"]')
  ).toHaveAttribute("data-variant", "illustrated")
  fireEvent.click(screen.getByRole("button", { name: "Refresh events" }))
  expect(refresh).toHaveBeenCalledOnce()
})
it.each([
  [403, "Event access denied"],
  [404, "Events unavailable"],
  [500, "Could not load events"],
])(
  "keeps error %s separate from empty and suppresses cached events",
  (statusCode, title) => {
    render(
      <EventJournal
        projectID="project"
        page={{ items: [item], replayTruncated: false }}
        error={{ statusCode, message: "private-token" }}
        retry={retry}
      />
    )
    expect(screen.getByText(title)).toBeInTheDocument()
    expect(screen.queryByText("No events yet")).not.toBeInTheDocument()
    expect(screen.queryByText("operation-1")).not.toBeInTheDocument()
    expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Retry events" }) != null).toBe(
      statusCode === 500
    )
  }
)
it("renders causal generation, operation and state without claiming completion", () => {
  render(
    <EventJournal
      projectID="project"
      page={{ items: [item], replayTruncated: false }}
      retry={retry}
    />
  )
  const journal = screen.getByRole("region", { name: "Event history" })
  expect(within(journal).getByText("Waiting approval")).toBeInTheDocument()
  expect(within(journal).getByText("Approval required")).toBeInTheDocument()
  expect(within(journal).getByText("Generation 2")).toBeInTheDocument()
  expect(within(journal).getByText("Operation operation-1")).toBeInTheDocument()
  expect(within(journal).queryByText("Completed")).not.toBeInTheDocument()
})
it("shows scaling demand with a project-scoped Cloud link", () => {
  render(
    <EventJournal
      projectID="project"
      page={{
        replayTruncated: false,
        items: [
          {
            ...item,
            lifecycle: undefined,
            kind: "scaling",
            scaling: {
              service: "api",
              previousReplicas: 1,
              desiredReplicas: 3,
              reason: "capacity_wait",
              writer: "manual",
              policyRevision: 2,
              cloudId: "cloud-1",
            },
          },
        ],
      }}
      retry={retry}
    />
  )
  expect(screen.getByText("api: 1 to 3 replicas requested")).toBeInTheDocument()
  expect(screen.getByText("Capacity wait")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Cloud capacity" })).toHaveAttribute(
    "href",
    "/clouds/cloud-1?project=project&tab=capacity"
  )
})
it("shows release target generation separately from the journal generation", () => {
  render(
    <EventJournal
      projectID="project"
      page={{
        replayTruncated: false,
        items: [
          {
            ...item,
            lifecycle: undefined,
            kind: "release",
            release: {
              campaignId: "campaign-1",
              wave: 1,
              state: "paused",
              reason: "health_unknown",
              targetGeneration: 3,
              targetOperationId: "operation-2",
            },
          },
        ],
      }}
      retry={retry}
    />
  )
  expect(screen.getByText("Paused")).toBeInTheDocument()
  expect(screen.getByText("Health unknown")).toBeInTheDocument()
  expect(screen.getByText("Target generation 3")).toBeInTheDocument()
  expect(screen.getByText("Target operation operation-2")).toBeInTheDocument()
})
it("keeps truncated history visible and requests only the next page", () => {
  const loadMore = vi.fn(async () => {})
  const props = {
    projectID: "project",
    page: { items: [item], replayTruncated: true, nextCursor: "next-page" },
    retry,
    loadMore,
  }
  const { rerender } = render(<EventJournal {...props} />)
  expect(screen.getByRole("status")).toHaveTextContent(
    "Earlier events are no longer available in this journal."
  )
  fireEvent.click(screen.getByRole("button", { name: "Next events" }))
  expect(loadMore).toHaveBeenCalledExactlyOnceWith("next-page")
  rerender(<EventJournal {...props} loadingMore />)
  expect(
    screen.getByRole("button", { name: "Loading next events" })
  ).toBeDisabled()
})

it("distinguishes an expired journal from a new empty journal", () => {
  render(
    <EventJournal
      projectID="project"
      page={{ items: [], replayTruncated: true }}
      retry={retry}
    />
  )
  expect(screen.getByText("No retained events")).toBeInTheDocument()
  expect(screen.queryByText("No events yet")).not.toBeInTheDocument()
})
it("distinguishes an empty continuation and keeps its resume cursor usable", () => {
  const loadMore = vi.fn(async () => {})
  render(
    <EventJournal
      projectID="project"
      continuation
      page={{ items: [], replayTruncated: false, nextCursor: "resume" }}
      retry={retry}
      loadMore={loadMore}
    />
  )
  expect(screen.getByText("No later events")).toBeInTheDocument()
  expect(screen.queryByText("No events yet")).not.toBeInTheDocument()
  fireEvent.click(
    screen.getByRole("button", { name: "Check for later events" })
  )
  expect(loadMore).toHaveBeenCalledExactlyOnceWith("resume")
})
it("keeps a retention gap resume cursor usable on an empty page", () => {
  const loadMore = vi.fn(async () => {})
  render(
    <EventJournal
      projectID="project"
      continuation
      page={{ items: [], replayTruncated: true, nextCursor: "after-expired" }}
      retry={retry}
      loadMore={loadMore}
    />
  )
  expect(screen.getByText("No retained events")).toBeInTheDocument()
  fireEvent.click(
    screen.getByRole("button", { name: "Check for later events" })
  )
  expect(loadMore).toHaveBeenCalledExactlyOnceWith("after-expired")
})
