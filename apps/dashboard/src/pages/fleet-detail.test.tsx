import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { FleetDetail } from "./fleet-detail"
import { InstanceDetail } from "./instance-detail"

vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
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
})

const row = {
  id: "instance-a",
  fleet_id: "fleet-a",
  external_ref: "sample-a",
  status: "waiting_approval",
  stage_id: "stage-a",
  environment_id: "environment-a",
  profile_id: "small",
  revision: 1,
  desired_generation: 1,
  observed_generation: 0,
  ownership_epoch: 1,
  desired_release_id: "release-a",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}
function serve(
  options: {
    status?: number
    items?: unknown[]
    instanceStatus?: () => number
  } = {}
) {
  const reads: URL[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    reads.push(url)
    if (url.pathname.endsWith("/instances/instance-a")) {
      const status = options.instanceStatus?.() ?? 200
      return Response.json(
        status === 200
          ? row
          : { error: "private-provider-error", code: status },
        { status }
      )
    }
    if (url.pathname.endsWith("/instances"))
      return Response.json({
        items: options.items ?? [row],
        next_cursor: "cursor-next",
      })
    const status = options.status ?? 200
    return Response.json(
      status === 200
        ? {
            id: "fleet-a",
            org_id: "org-a",
            project_id: "project-a",
            revision: 1,
            status: "ready",
          }
        : { error: "private-provider-error", code: status },
      { status }
    )
  })
  return reads
}
const wrap = (children: ReactNode) => (
  <ClientProvider client={forgeClient}>{children}</ClientProvider>
)

it("decodes the generated wire fields and keeps pagination and filters project-scoped", async () => {
  const reads = serve()
  render(wrap(<FleetDetail projectID="project-a" />))
  expect(await screen.findByRole("link", { name: "sample-a" })).toHaveAttribute(
    "href",
    "/projects/project-a/fleet/instances/instance-a"
  )
  expect(screen.getByText("Target: release-a")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Next page" }))
  await waitFor(() =>
    expect(
      reads.some((url) => url.searchParams.get("after") === "cursor-next")
    ).toBe(true)
  )
  fireEvent.change(screen.getByLabelText("Instance reference"), {
    target: { value: "sample-b" },
  })
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }))
  await waitFor(() =>
    expect(
      reads.some(
        (url) =>
          url.searchParams.get("external_ref") === "sample-b" &&
          !url.searchParams.get("after")
      )
    ).toBe(true)
  )
  expect(
    reads.every((url) =>
      url.pathname.startsWith("/api/v1/projects/project-a/fleet")
    )
  ).toBe(true)
})

it("does not request instance rows after fleet access is denied", async () => {
  const reads = serve({ status: 403 })
  render(wrap(<FleetDetail projectID="project-a" />))
  expect(await screen.findByText("Fleet access denied")).toBeInTheDocument()
  expect(reads.some((url) => url.pathname.endsWith("/instances"))).toBe(false)
  expect(screen.queryByText("private-provider-error")).not.toBeInTheDocument()
})

it("renders the shared empty state from an empty generated response", async () => {
  serve({ items: [] })
  render(wrap(<FleetDetail projectID="project-a" />))
  expect(await screen.findByText("No instances yet")).toBeInTheDocument()
  expect(screen.queryByRole("table")).not.toBeInTheDocument()
})

it("removes cached instance details when a fresh read revokes access", async () => {
  let status = 200
  serve({ instanceStatus: () => status })
  render(wrap(<InstanceDetail projectID="project-a" instanceID="instance-a" />))
  expect(
    await screen.findByRole("heading", { name: "sample-a" })
  ).toBeInTheDocument()
  expect(screen.getByLabelText("Observed release")).toHaveTextContent("Unknown")
  status = 403
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
  expect(await screen.findByText("Fleet access denied")).toBeInTheDocument()
  expect(
    screen.queryByRole("heading", { name: "sample-a" })
  ).not.toBeInTheDocument()
  expect(screen.queryByLabelText("Desired release")).not.toBeInTheDocument()
  expect(screen.queryByText("private-provider-error")).not.toBeInTheDocument()
})
