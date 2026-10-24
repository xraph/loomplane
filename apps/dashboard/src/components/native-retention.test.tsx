import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { NativeRetention } from "@/components/native-retention"

const access = vi.hoisted(() => ({ owner: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (permission: string) =>
      permission !== "service_backup:retire" || access.owner,
    session: { orgId: "org", userId: "owner" },
  }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.owner = true
})
function fixture({ complete = true, lose = false, deny = false } = {}) {
  const writes: { body: Record<string, unknown>; url: string }[] = []
  const job = {
    id: "deletion",
    org_id: "org",
    project_id: "project",
    source_id: "source",
    point_id: "old",
    witness_id: "tested",
    status: "failed",
    policy: { older_than_days: 30, keep_last: 7 },
    created_by: "owner",
    created_at: "2026-10-23T00:00:00Z",
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let body: unknown
      if (url.includes("retention-preview"))
        body = {
          as_of: "2026-10-23T00:00:00Z",
          complete,
          eligible_count: 1,
          eligible_bytes: 0,
          verified_backup_id: "tested",
          points: [
            {
              backup_id: "old",
              created_at: "2026-09-01T00:00:00Z",
              eligible: true,
              deletion_supported: true,
              bytes: 0,
              reason: "eligible",
            },
          ],
        }
      else if (init?.method === "POST") {
        writes.push({
          body: init.body ? JSON.parse(String(init.body)) : {},
          url,
        })
        if (deny)
          return new Response(
            JSON.stringify({ message: "Owner permission was revoked" }),
            { status: 403, headers: { "Content-Type": "application/json" } }
          )
        if (lose && writes.length === 1) throw new TypeError("response lost")
        body = job
      } else if (url.includes("/history"))
        body = {
          events: [
            {
              cursor: "1",
              kind: "admitted",
              status: "queued",
              actor: "owner",
              created_at: "2026-10-23T00:00:00Z",
            },
          ],
        }
      else if (url.endsWith("/retirements?limit=25"))
        body = { retirements: [job] }
      else body = job
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return writes
}
async function show() {
  render(
    <ClientProvider client={forgeClient}>
      <NativeRetention
        source="source"
        project="project"
        onChanged={async () => {}}
      />
    </ClientProvider>
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Review native retention" })
  )
  await screen.findByText("old")
}
it("confirms native deletion and reuses the request after an uncertain response", async () => {
  const writes = fixture({ lose: true })
  await show()
  await userEvent.click(
    screen.getByRole("button", { name: "Delete native backup" })
  )
  expect(writes).toHaveLength(0)
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native deletion" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /not confirmed|response lost/i
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native deletion" })
  )
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0].body.request_id).toBeTruthy()
  expect(writes[1].body).toEqual(writes[0].body)
  expect(writes[0].body).toMatchObject({
    policy: { older_than_days: 30, keep_last: 7 },
    acknowledge_deletion: true,
  })
  expect(
    (await screen.findAllByText(/deletion needs attention/i))[0]
  ).toBeInTheDocument()
})
it("keeps incomplete and read-only previews free of deletion actions", async () => {
  fixture({ complete: false })
  await show()
  expect(
    screen.queryByRole("button", { name: "Delete native backup" })
  ).not.toBeInTheDocument()
  expect(screen.getByText(/incomplete/i)).toBeInTheDocument()
})
it("lets readers inspect source deletion history without retrying", async () => {
  access.owner = false
  fixture()
  await show()
  expect(
    screen.queryByRole("button", { name: "Delete native backup" })
  ).not.toBeInTheDocument()
  await userEvent.click(
    await screen.findByRole("button", { name: "Deletion details" })
  )
  expect(await screen.findByText("admitted")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Retry native deletion" })
  ).not.toBeInTheDocument()
})
it("shows a server permission failure without claiming deletion", async () => {
  fixture({ deny: true })
  await show()
  await userEvent.click(
    screen.getByRole("button", { name: "Delete native backup" })
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native deletion" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /permission.*revoked/i
  )
  expect(
    screen.getByRole("button", { name: "Confirm native deletion" })
  ).toBeEnabled()
})
