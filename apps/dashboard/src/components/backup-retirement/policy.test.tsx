import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { useRetentionPoliciesGet } from "@/client/retention-policies"
import { RetentionPolicyControls } from "./policy"

const permission = vi.hoisted(() => ({ owner: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permission.owner }),
}))
afterEach(() => {
  permission.owner = true
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture({
  enabled = false,
  configured = false,
  blocked = null as string | null,
  loseResponse = false,
  inspectionDenied = false,
  pagedHistory = false,
} = {}) {
  let policy = {
    id: "policy",
    revision: configured ? 3 : 0,
    enabled,
    policy: { older_than_days: 7, keep_last: 1 },
    authorized_by: "owner",
    created_at: "2026-10-20T12:00:00Z",
    updated_at: "2026-10-20T12:00:00Z",
    last_retirement_id: "last-deletion",
  }
  let exists = configured
  const writes: Record<string, unknown>[] = []
  const historyRequests: string[] = []
  const changed = vi.fn(async () => undefined)
  const show = vi.fn()
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (
        !url.includes("/history") &&
        init?.method !== "PUT" &&
        inspectionDenied
      )
        return new Response(JSON.stringify({ error: "Policy access denied" }), {
          status: 403,
          headers: { "Content-Type": "application/json" },
        })
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body))
        writes.push(body)
        exists = true
        policy = { ...policy, ...body, revision: body.revision + 1 }
        if (loseResponse)
          return new Response(JSON.stringify({ error: "Response uncertain" }), {
            status: 503,
            headers: { "Content-Type": "application/json" },
          })
        return new Response(JSON.stringify(policy), {
          headers: { "Content-Type": "application/json" },
        })
      }
      const older = new URL(url, "http://localhost").searchParams.get("before")
      if (url.includes("/history")) historyRequests.push(older ?? "")
      const body = url.includes("/history")
        ? {
            events: exists
              ? [
                  {
                    revision: older ? 1 : 3,
                    enabled,
                    policy: policy.policy,
                    authorized_by: "owner",
                    created_at: "2026-10-20T12:00:00Z",
                  },
                ]
              : [],
            ...(pagedHistory && !older ? { next_before: "2" } : {}),
          }
        : { configured: exists, ...(exists ? { policy } : {}) }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  const view = render(
    <ClientProvider client={forgeClient}>
      <RetentionPolicyControls
        project="project"
        anchor="backup"
        settings={{ olderThanDays: 30, keepLast: 7 }}
        enableBlocked={blocked}
        onChanged={changed}
        onOpenRetirement={show}
      />
    </ClientProvider>
  )
  return {
    unmount: view.unmount,
    writes,
    historyRequests,
    changed,
    show,
    allowInspection: () => {
      inspectionDenied = false
    },
    advance: () => {
      policy = { ...policy, revision: 4 }
    },
  }
}

it("requires explicit confirmation to enable the reviewed settings and revision", async () => {
  const { writes } = fixture()
  await userEvent.click(
    await screen.findByRole("button", { name: "Enable automatic deletion" })
  )
  expect(writes).toHaveLength(0)
  const dialog = screen.getByRole("alertdialog")
  expect(within(dialog).getByText(/30 days/)).toBeInTheDocument()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Enable automatic deletion" })
  )
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    revision: 0,
    enabled: true,
    policy: { older_than_days: 30, keep_last: 7 },
  })
  expect(
    await screen.findByText("Automatic deletion enabled")
  ).toBeInTheDocument()
})

it("allows pause without storage and preserves the saved rule", async () => {
  const { writes } = fixture({
    enabled: true,
    configured: true,
    blocked: "Storage is unavailable",
  })
  await userEvent.click(
    await screen.findByRole("button", { name: "Pause automatic deletion" })
  )
  const dialog = screen.getByRole("alertdialog")
  expect(within(dialog).getByText(/already accepted/i)).toBeInTheDocument()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Pause automatic deletion" })
  )
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    revision: 3,
    enabled: false,
    policy: { older_than_days: 7, keep_last: 1 },
  })
  expect(
    await screen.findByText("Automatic deletion paused")
  ).toBeInTheDocument()
  await waitFor(() =>
    expect(
      screen.getByRole("region", { name: "Automatic retention policy" })
    ).toHaveFocus()
  )
})

it("lets readers inspect changes and the last deletion without write controls", async () => {
  permission.owner = false
  const { writes, show } = fixture({ enabled: true, configured: true })
  await screen.findByText("Automatic deletion enabled")
  expect(
    screen.queryByRole("button", { name: "Pause automatic deletion" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Update automatic policy" })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Policy changes" }))
  expect(await screen.findByText("Revision 3")).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Last deletion" }))
  expect(show).toHaveBeenCalledWith("last-deletion")
  expect(writes).toHaveLength(0)
})

it("does not rebase an open confirmation when policy progress is refreshed", async () => {
  const { advance, writes } = fixture({ configured: true })
  await userEvent.click(
    await screen.findByRole("button", { name: "Enable automatic deletion" })
  )
  advance()
  await useRetentionPoliciesGet({ path: { id: "backup" } }).refetch()
  expect(await screen.findByText(/policy changed while/i)).toBeInTheDocument()
  expect(
    within(screen.getByRole("alertdialog")).getByRole("button", {
      name: "Enable automatic deletion",
    })
  ).toBeDisabled()
  expect(writes).toHaveLength(0)
})

it("inspects an uncertain save before allowing a new grant", async () => {
  const { writes } = fixture({ loseResponse: true })
  await userEvent.click(
    await screen.findByRole("button", { name: "Enable automatic deletion" })
  )
  await userEvent.click(
    within(screen.getByRole("alertdialog")).getByRole("button", {
      name: "Enable automatic deletion",
    })
  )
  expect(await screen.findByText(/Response uncertain/)).toBeInTheDocument()
  expect(writes).toHaveLength(1)
  await userEvent.click(
    screen.getByRole("button", { name: "Inspect current policy" })
  )
  expect(
    await screen.findByText("Automatic deletion enabled")
  ).toBeInTheDocument()
  await waitFor(() =>
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  )
  expect(writes).toHaveLength(1)
})

it("rechecks cached policy state when the review is reopened", async () => {
  const first = fixture()
  await screen.findByText("No automatic deletion policy")
  first.unmount()
  fixture({ configured: true, enabled: true })
  expect(
    await screen.findByText("Automatic deletion enabled")
  ).toBeInTheDocument()
})

it("keeps denied inspection distinct from an unset policy and allows recovery", async () => {
  const { allowInspection, writes } = fixture({ inspectionDenied: true })
  expect(await screen.findByText("Policy access denied.")).toBeInTheDocument()
  expect(
    screen.queryByText("No automatic deletion policy")
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Enable automatic deletion" })
  ).not.toBeInTheDocument()
  allowInspection()
  await userEvent.click(
    screen.getByRole("button", { name: "Retry policy inspection" })
  )
  expect(
    await screen.findByText("No automatic deletion policy")
  ).toBeInTheDocument()
  expect(writes).toHaveLength(0)
})

it("pages saved revisions without mixing their query state", async () => {
  const { historyRequests } = fixture({ configured: true, pagedHistory: true })
  await userEvent.click(
    await screen.findByRole("button", { name: "Policy changes" })
  )
  expect(await screen.findByText("Revision 3")).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Older changes" }))
  expect(await screen.findByText("Revision 1")).toBeInTheDocument()
  expect(screen.queryByText("Revision 3")).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Newer changes" }))
  expect(await screen.findByText("Revision 3")).toBeInTheDocument()
  expect(historyRequests).toContain("2")
})
