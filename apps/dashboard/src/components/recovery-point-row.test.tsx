import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import type { Backup } from "@/client/forge/loomplane/types"
import { Table, TableBody } from "@/components/ui/table"
import { RecoveryPointRow } from "@/components/recovery-point-row"
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: { userId: "owner" } }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("shows a failed deletion on its own backup and keeps restore blocked", async () => {
  const reads: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    reads.push(url)
    const query = new URL(url, "http://localhost").searchParams
    expect(query.get("backup_id")).toBe("blocked-point")
    return new Response(
      JSON.stringify({
        retirements: [
          {
            id: "retirement",
            project_id: "project",
            backup_id: "blocked-point",
            status: "failed",
            error: "Retained file could not be verified.",
            created_at: "2026-10-20T12:00:00Z",
          },
        ],
      }),
      { headers: { "Content-Type": "application/json" } }
    )
  })
  render(
    <ClientProvider client={forgeClient}>
      <Table>
        <TableBody>
          <RecoveryPointRow
            project="project"
            backup={
              {
                id: "blocked-point",
                status: "retiring",
                name: "database",
                createdAt: "2026-09-01T12:00:00Z",
                bytes: 4096,
              } as Backup
            }
            busy={false}
            onRestore={vi.fn()}
            onRetry={vi.fn()}
            onChanged={async () => undefined}
          />
        </TableBody>
      </Table>
    </ClientProvider>
  )
  expect(
    (await screen.findAllByText("Deletion needs attention")).length
  ).toBeGreaterThan(0)
  expect(screen.getByRole("button", { name: "Deletion details" })).toBeEnabled()
  expect(
    screen.queryByRole("button", { name: "Test restore" })
  ).not.toBeInTheDocument()
  expect(reads).toHaveLength(1)
})

it("does not fetch every historical deletion until details are opened", () => {
  const fetch = vi.fn()
  vi.stubGlobal("fetch", fetch)
  render(
    <ClientProvider client={forgeClient}>
      <Table>
        <TableBody>
          <RecoveryPointRow
            project="project"
            backup={
              {
                id: "deleted-point",
                status: "retired",
                name: "database",
                createdAt: "2026-09-01T12:00:00Z",
                bytes: 4096,
              } as Backup
            }
            busy={false}
            onRestore={vi.fn()}
            onRetry={vi.fn()}
            onChanged={async () => undefined}
          />
        </TableBody>
      </Table>
    </ClientProvider>
  )
  expect(screen.getAllByText("Backup file deleted").length).toBeGreaterThan(0)
  expect(
    screen.queryByRole("button", { name: "Test restore" })
  ).not.toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalled()
})

it("keeps deletion details open when its backup becomes retired", async () => {
  const { default: userEvent } = await import("@testing-library/user-event")
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    const operation = {
      id: "retirement",
      project_id: "project",
      backup_id: "blocked-point",
      witness_id: "witness",
      status: "succeeded",
      error: "Previous deletion attempt failed.",
      created_by: "owner",
      execution_by: "owner",
      attempt: 1,
      attempt_limit: 3,
      created_at: "2026-10-20T12:00:00Z",
      policy: { older_than_days: 7, keep_last: 1 },
    }
    const body = url.includes("/history")
      ? { events: [] }
      : url.includes("/retention-preview")
        ? { complete: true, points: [] }
        : url.endsWith("/service-backups/witness")
          ? {
              id: "witness",
              status: "succeeded",
              bytes: 100,
              created_at: "2026-10-01T12:00:00Z",
            }
          : url.includes("/service-backup-retirements?")
            ? { retirements: [operation] }
            : operation
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
  const backup = {
    id: "blocked-point",
    status: "retiring",
    name: "database",
    createdAt: "2026-09-01T12:00:00Z",
    bytes: 4096,
  } as Backup
  const changed = async () => undefined
  const draw = (point: Backup) => (
    <ClientProvider client={forgeClient}>
      <Table>
        <TableBody>
          <RecoveryPointRow
            project="project"
            backup={point}
            busy={false}
            onRestore={vi.fn()}
            onRetry={vi.fn()}
            onChanged={changed}
          />
        </TableBody>
      </Table>
    </ClientProvider>
  )
  const view = render(draw(backup))
  await screen.findAllByText("Backup file deleted")
  expect(
    screen.queryAllByText("Previous deletion attempt failed.")
  ).toHaveLength(0)
  await userEvent.click(
    screen.getByRole("button", { name: "Deletion details" })
  )
  await screen.findByRole("dialog", { name: "Backup deletion" })
  view.rerender(draw({ ...backup, status: "retired" }))
  expect(
    screen.getByRole("dialog", { name: "Backup deletion" })
  ).toBeInTheDocument()
})

it.each([
  ["succeeded", "Verified today"],
  ["retired", "Previously verified today"],
])(
  "uses compact dates while retaining %s restore evidence",
  (status, evidence) => {
    render(
      <ClientProvider client={forgeClient}>
        <Table>
          <TableBody>
            <RecoveryPointRow
              compact
              project="project"
              backup={
                {
                  id: "point",
                  name: "database",
                  status,
                  bytes: 124000000,
                  createdAt: new Date(Date.now() - 7200000).toISOString(),
                  restoredAt: new Date().toISOString(),
                } as Backup
              }
              busy={false}
              onRestore={vi.fn()}
              onRetry={vi.fn()}
              onChanged={async () => undefined}
            />
          </TableBody>
        </Table>
      </ClientProvider>
    )
    expect(screen.getByText("2 hours ago")).toBeVisible()
    expect(screen.getByText("124 MB")).toBeVisible()
    expect(screen.getByText(evidence)).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Restore copy" }) !== null
    ).toBe(status === "succeeded")
  }
)
