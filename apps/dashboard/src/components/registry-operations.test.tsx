import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import type { Connection } from "@/client/forge/loomplane/types"
import { RegistryOperations } from "./registry-operations"

const connection: Connection = {
  id: "conn",
  cloud: "kubernetes",
  generation: 4,
  details: { registry_hostname: "images.example.com" },
  account: "local-cluster",
  createdAt: "2026-10-29T12:00:00Z",
  createdBy: "manager",
  message: "",
  method: "token",
  missing: [],
  name: "Development cluster",
  orgId: "org",
  status: "ready",
  updatedAt: "2026-10-29T12:00:00Z",
}
const status = {
  connection_id: "conn",
  generation: 4,
  hostname: "images.example.com",
  status: "ready",
  message: "",
  action: "",
  phase: "",
  dns_ready: true,
  backups: [],
}
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
function mount(state = status) {
  const posted: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: unknown, init?: RequestInit) => {
      if (init?.method === "POST") {
        posted.push(JSON.parse(String(init.body)) as Record<string, unknown>)
      }
      return Response.json(state)
    })
  )
  render(
    <ClientProvider client={forgeClient}>
      <RegistryOperations connection={connection} mayManage />
    </ClientProvider>
  )
  return posted
}
it("requires hostname confirmation and preserves data by default", async () => {
  const posted = mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole("button", { name: "Registry operations" }))
  await user.click(
    await screen.findByRole("button", { name: "Remove registry" })
  )
  const confirm = screen.getByRole("button", { name: "Confirm remove" })
  expect(confirm).toBeDisabled()
  await user.type(
    screen.getByLabelText("Type images.example.com to confirm"),
    "images.example.com"
  )
  await user.click(confirm)
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0]).toMatchObject({
    kind: "remove",
    hostname: "images.example.com",
    generation: 4,
    delete_data: false,
    remove_setup: false,
  })
})
it("keeps failed operations retryable without queuing another operation", async () => {
  const posted = mount({
    ...status,
    status: "needs_attention",
    action: "restore",
    phase: "backed_up",
  })
  const user = userEvent.setup()
  await user.click(screen.getByRole("button", { name: "Registry operations" }))
  expect(
    await screen.findByRole("button", { name: "Back up now" })
  ).toBeDisabled()
  await user.click(
    screen.getByRole("button", { name: "Review and retry current operation" })
  )
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0]).toMatchObject({
    kind: "retry",
    generation: 4,
    hostname: "images.example.com",
  })
})
