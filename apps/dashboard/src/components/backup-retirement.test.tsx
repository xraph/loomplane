import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import type { Backup } from "@/client/forge/loomplane/types"
import { BackupRetentionReview } from "@/components/backup-retirement/review"

const permission = vi.hoisted(() => ({ retire: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (name: string) =>
      name !== "service_backup:retire" || permission.retire,
    session: { userId: "owner" },
  }),
}))
afterEach(() => {
  permission.retire = true
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const points = [
  {
    id: "old-point",
    name: "database",
    environmentId: "environment",
    status: "succeeded",
    bytes: 4096,
    createdAt: "2026-09-01T12:00:00Z",
  },
  {
    id: "tested-point",
    name: "database",
    environmentId: "environment",
    status: "succeeded",
    bytes: 8192,
    createdAt: "2026-10-01T12:00:00Z",
    restoredAt: "2026-10-02T12:00:00Z",
  },
] as Backup[]

function fixture({
  supported = true,
  complete = true,
  loseResponse = false,
} = {}) {
  const writes: { url: string; body: Record<string, unknown> }[] = []
  let accepted = false
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const operation = {
        id: "retirement",
        project_id: "project",
        org_id: "org",
        backup_id: "old-point",
        witness_id: "tested-point",
        status: "succeeded",
        error: "Previous attempt failed.",
        execution_by: "owner",
        created_by: "owner",
        attempt: 1,
        attempt_limit: 3,
        created_at: "2026-10-20T12:00:00Z",
        policy: { older_than_days: 30, keep_last: 7 },
      }
      let body: unknown = {}
      if (url.includes("/retention-preview"))
        body = {
          complete,
          as_of: "2026-10-20T12:00:00Z",
          eligible_count: 1,
          eligible_bytes: 4096,
          points: [
            {
              backup_id: "old-point",
              bytes: 4096,
              eligible: true,
              reason: "eligible",
              deletion_supported: supported,
              deletion_unavailable_reason: supported
                ? undefined
                : "This storage cannot delete captured versions.",
            },
            {
              backup_id: "tested-point",
              bytes: 8192,
              eligible: false,
              reason: "last_verified",
              deletion_supported: supported,
            },
          ],
        }
      if (init?.method === "POST") {
        const payload = JSON.parse(String(init.body))
        writes.push({ url, body: payload })
        if (loseResponse && writes.length === 1)
          return new Response(
            JSON.stringify({ error: "Response lost. Retry this request." }),
            { status: 503, headers: { "Content-Type": "application/json" } }
          )
        accepted = true
        body = operation
      } else if (url.includes("/service-backup-retirements/retirement/history"))
        body = {
          events: [
            {
              cursor: "3",
              kind: "succeeded",
              status: "succeeded",
              actor: "owner",
              attempt: 1,
              attempt_limit: 3,
              created_at: "2026-10-20T12:00:00Z",
            },
          ],
        }
      else if (url.endsWith("/service-backup-retirements/retirement"))
        body = operation
      else if (url.includes("/service-backup-retirements"))
        body = { retirements: accepted ? [operation] : [] }
      else if (url.endsWith("/service-backups/tested-point"))
        body = {
          ...points[1],
          created_at: points[1].createdAt,
          restored_at: points[1].restoredAt,
          environment_id: "environment",
        }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <BackupRetentionReview
        project="project"
        points={points}
        onChanged={async () => undefined}
      />
    </ClientProvider>
  )
  return writes
}

it("reviews the captured storage capability and confirms only one eligible deletion", async () => {
  const writes = fixture()
  await userEvent.click(
    screen.getByRole("button", { name: "Review retention" })
  )
  const review = await screen.findByRole("dialog", { name: "Backup retention" })
  expect(
    await within(review).findByText("Latest restore-tested recovery point")
  ).toBeInTheDocument()
  await userEvent.click(
    within(review).getByRole("button", { name: "Delete backup" })
  )
  const confirmation = screen.getByRole("alertdialog")
  expect(writes).toHaveLength(0)
  await userEvent.click(
    within(confirmation).getByRole("button", { name: "Delete backup" })
  )
  const details = await screen.findByRole("dialog", { name: "Backup deletion" })
  await within(details).findAllByText("Backup file deleted")
  expect(within(details).queryByRole("alert")).not.toBeInTheDocument()
  expect(writes).toEqual([
    {
      url: expect.stringContaining("/service-backups/old-point/retire"),
      body: {
        request_id: expect.any(String),
        policy: { older_than_days: 30, keep_last: 7 },
      },
    },
  ])
})

it.each([
  { supported: false, complete: true },
  { supported: true, complete: false },
])(
  "does not offer deletion from an unsupported or incomplete review: %j",
  async (settings) => {
    fixture(settings)
    await userEvent.click(
      screen.getByRole("button", { name: "Review retention" })
    )
    const review = await screen.findByRole("dialog", {
      name: "Backup retention",
    })
    await within(review).findByText("Latest restore-tested recovery point")
    expect(
      within(review).queryByRole("button", { name: "Delete backup" })
    ).not.toBeInTheDocument()
  }
)

it("lets readers inspect retention without offering an owner action", async () => {
  permission.retire = false
  fixture()
  await userEvent.click(
    screen.getByRole("button", { name: "Review retention" })
  )
  const review = await screen.findByRole("dialog", { name: "Backup retention" })
  await within(review).findByText("Latest restore-tested recovery point")
  expect(
    within(review).queryByRole("button", { name: "Delete backup" })
  ).not.toBeInTheDocument()
})

it("requires an updated preview after settings are edited", async () => {
  fixture()
  await userEvent.click(
    screen.getByRole("button", { name: "Review retention" })
  )
  const review = await screen.findByRole("dialog", { name: "Backup retention" })
  await within(review).findByRole("button", { name: "Delete backup" })
  await userEvent.clear(
    within(review).getByRole("spinbutton", { name: "Older than (days)" })
  )
  await userEvent.type(
    within(review).getByRole("spinbutton", { name: "Older than (days)" }),
    "7"
  )
  expect(
    within(review).getByRole("button", { name: "Delete backup" })
  ).toBeDisabled()
})

it("preserves the request key when a response is lost", async () => {
  const writes = fixture({ loseResponse: true })
  await userEvent.click(
    screen.getByRole("button", { name: "Review retention" })
  )
  const review = await screen.findByRole("dialog", { name: "Backup retention" })
  await userEvent.click(
    await within(review).findByRole("button", { name: "Delete backup" })
  )
  const confirmation = screen.getByRole("alertdialog")
  await userEvent.click(
    within(confirmation).getByRole("button", { name: "Delete backup" })
  )
  await within(confirmation).findByText(/response lost/i)
  await userEvent.click(
    within(confirmation).getByRole("button", { name: "Delete backup" })
  )
  expect(writes).toHaveLength(2)
  expect(writes[1].body).toEqual(writes[0].body)
})

it.each([true, false])(
  "shows failed deletion history with owner permission %s",
  async (owner) => {
    permission.retire = owner
    const { RetirementDetails } =
      await import("@/components/backup-retirement/details")
    const writes: unknown[] = []
    const reads: string[] = []
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input)
        reads.push(url)
        let body: unknown = {
          id: "retirement",
          backup_id: "old-point",
          witness_id: "tested-point",
          status: "failed",
          error: "Retained file could not be verified.",
          attempt: 3,
          attempt_limit: 3,
          created_by: "previous-owner",
          execution_by: "previous-owner",
          created_at: "2026-10-20T12:00:00Z",
          policy: { older_than_days: 30, keep_last: 7 },
        }
        if (url.includes("/history"))
          body = {
            events: [
              {
                cursor: "3",
                kind: "failed",
                status: "failed",
                actor: "previous-owner",
                attempt: 3,
                attempt_limit: 3,
                error: "Retained file could not be verified.",
                created_at: "2026-10-20T12:00:00Z",
              },
            ],
            next_before: "3",
          }
        else if (url.includes("/retention-preview"))
          body = {
            complete: true,
            eligible_count: 0,
            eligible_bytes: 0,
            points: [
              {
                backup_id: "old-point",
                bytes: 4096,
                eligible: false,
                reason: "not_complete",
                deletion_supported: true,
              },
            ],
          }
        else if (url.endsWith("/service-backups/tested-point"))
          body = {
            id: "tested-point",
            status: "succeeded",
            bytes: 8192,
            created_at: "2026-10-01T12:00:00Z",
            restored_at: "2026-10-02T12:00:00Z",
          }
        if (init?.method === "POST") {
          writes.push(JSON.parse(String(init.body)))
          body = {
            ...(body as object),
            id: "retirement",
            status: "queued",
            execution_by: "owner",
          }
        }
        return new Response(JSON.stringify(body), {
          headers: { "Content-Type": "application/json" },
        })
      }
    )
    render(
      <ClientProvider client={forgeClient}>
        <RetirementDetails
          project="project"
          id="retirement"
          onChanged={async () => undefined}
          onClose={() => undefined}
        />
      </ClientProvider>
    )
    const dialog = await screen.findByRole("dialog", {
      name: "Backup deletion",
    })
    expect(
      await within(dialog).findByText("Deletion needs attention")
    ).toBeInTheDocument()
    await userEvent.click(
      await within(dialog).findByRole("button", { name: "Older events" })
    )
    await waitFor(() =>
      expect(
        reads.some(
          (url) =>
            new URL(url, "http://localhost").searchParams.get("before") === "3"
        )
      ).toBe(true)
    )
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Newer events" })
    )
    if (!owner) {
      expect(
        within(dialog).queryByRole("button", { name: "Retry deletion" })
      ).not.toBeInTheDocument()
      expect(writes).toHaveLength(0)
      return
    }
    const retryButton = await within(dialog).findByRole("button", {
      name: "Retry deletion",
    })
    await waitFor(() => expect(retryButton).toBeEnabled())
    await userEvent.click(retryButton)
    expect(writes).toHaveLength(0)
    await userEvent.click(
      within(screen.getByRole("alertdialog")).getByRole("button", {
        name: "Retry deletion",
      })
    )
    expect(writes).toEqual([{ request_id: expect.any(String) }])
  }
)
