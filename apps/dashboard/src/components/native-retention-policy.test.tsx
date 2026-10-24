import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { NativeRetentionPolicyControls } from "@/components/native-retention-policy"

const access = vi.hoisted(() => ({ owner: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => access.owner,
    session: { orgId: "org", userId: "owner" },
  }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.owner = true
})
function fixture({ enabled = false, fail = false } = {}) {
  let revision = enabled ? 1 : 0
  let active = enabled
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let body: unknown
      const policy = () => ({
        id: "policy",
        org_id: "org",
        project_id: "project",
        source_id: "source",
        revision,
        enabled: active,
        policy: { older_than_days: 7, keep_last: 2 },
        created_by: "owner",
        authorized_by: "owner",
        created_at: "2026-10-23T00:00:00Z",
        updated_at: "2026-10-23T00:00:00Z",
        next_at: "2026-10-23T00:00:00Z",
      })
      if (init?.method === "PUT") {
        const submitted = JSON.parse(String(init.body))
        writes.push(submitted)
        if (fail)
          return new Response(
            JSON.stringify({
              message: "Policy changed; inspect the current revision",
            }),
            { status: 409, headers: { "Content-Type": "application/json" } }
          )
        revision++
        active = submitted.enabled
        body = policy()
      } else if (url.includes("/history"))
        body = {
          events: revision
            ? [
                {
                  revision,
                  enabled: active,
                  policy: { older_than_days: 7, keep_last: 2 },
                  authorized_by: "owner",
                  created_at: "2026-10-23T00:00:00Z",
                },
              ]
            : [],
        }
      else
        body = {
          configured: revision > 0,
          ...(revision ? { policy: policy() } : {}),
        }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return writes
}
async function show(blocked: string | null = null) {
  render(
    <ClientProvider client={forgeClient}>
      <NativeRetentionPolicyControls
        source="source"
        project="project"
        settings={{ olderThanDays: 7, keepLast: 2 }}
        enableBlocked={blocked}
        onDetails={() => {}}
      />
    </ClientProvider>
  )
  await screen.findByText(/Automatic native deletion is (off|on)/)
}
it("requires confirmation for a revision-fenced automatic native deletion rule", async () => {
  const writes = fixture()
  await show()
  await userEvent.click(
    screen.getByRole("button", { name: "Enable automatic native deletion" })
  )
  expect(writes).toHaveLength(0)
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native policy" })
  )
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    revision: 0,
    enabled: true,
    policy: { older_than_days: 7, keep_last: 2 },
  })
  expect(
    await screen.findByText(/Automatic native deletion is on/)
  ).toBeInTheDocument()
})
it("can pause the saved rule while cloud preview is unavailable", async () => {
  const writes = fixture({ enabled: true })
  await show("The cloud history could not be read")
  await userEvent.click(
    screen.getByRole("button", { name: "Pause native retention" })
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native policy" })
  )
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    revision: 1,
    enabled: false,
    policy: { older_than_days: 7, keep_last: 2 },
  })
  expect(
    await screen.findByText(/Automatic native deletion is off/)
  ).toBeInTheDocument()
})
it("keeps reader access to revisions without policy write controls", async () => {
  access.owner = false
  fixture({ enabled: true })
  await show()
  expect(
    screen.queryByRole("button", { name: "Pause native retention" })
  ).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Native policy changes" })
  )
  expect(await screen.findByText(/Revision 1/)).toBeInTheDocument()
})
it("requires inspection after a conflicting policy save", async () => {
  fixture({ fail: true })
  await show()
  await userEvent.click(
    screen.getByRole("button", { name: "Enable automatic native deletion" })
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm native policy" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /inspect.*revision/i
  )
  expect(
    screen.getByRole("button", { name: "Inspect native policy" })
  ).toBeEnabled()
  expect(
    screen.queryByRole("button", { name: "Confirm native policy" })
  ).not.toBeInTheDocument()
})
