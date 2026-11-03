import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ApiKeys } from "@/pages/api-keys"
import { serve, wireNode } from "@/test-fixtures/nodes"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { userId: "u1", orgId: "org_one" },
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const key = {
  id: "key_one",
  name: "ci-deploy",
  hint: "ab12",
  access: "write",
  state: "active",
  created_by: "u1",
  created_at: "2026-10-01T00:00:00Z",
}

async function open(revokeKey: unknown) {
  const calls = serve({ nodes: [], keys: [key], revokeKey })
  const user = userEvent.setup()
  render(
    <ClientProvider client={forgeClient}>
      <ApiKeys />
    </ClientProvider>
  )
  await user.click(await screen.findByRole("button", { name: "Revoke" }))
  return { calls, user, dialog: await screen.findByRole("alertdialog") }
}

it("lists the joined nodes when compromised is ticked, and revokes them on request", async () => {
  const { calls, user, dialog } = await open({
    revoked: { ...key, state: "revoked" },
    joined_nodes: [
      wireNode({ id: "n_a", name: "home-nuc" }),
      wireNode({ id: "n_b", name: "build-box" }),
    ],
    joined_nodes_withheld: false,
  })
  await user.click(
    within(dialog).getByRole("checkbox", { name: /Revoke as compromised/ })
  )
  await user.click(within(dialog).getByRole("button", { name: "Revoke" }))

  expect(await screen.findByText("home-nuc")).toBeInTheDocument()
  expect(screen.getByText("build-box")).toBeInTheDocument()
  const del = calls.find((c) => c.method === "DELETE")
  expect(del?.url).toContain("compromised=true")

  await user.click(
    screen.getByRole("button", { name: "Revoke these nodes too" })
  )
  await vi.waitFor(() => {
    expect(calls.some((c) => c.url.endsWith("/n_a/revoke"))).toBe(true)
    expect(calls.some((c) => c.url.endsWith("/n_b/revoke"))).toBe(true)
  })
})

it("says so when the server withholds the nodes", async () => {
  const { user, dialog } = await open({
    revoked: { ...key, state: "revoked" },
    joined_nodes: [],
    joined_nodes_withheld: true,
  })
  await user.click(
    within(dialog).getByRole("checkbox", { name: /Revoke as compromised/ })
  )
  await user.click(within(dialog).getByRole("button", { name: "Revoke" }))
  expect(
    await screen.findByText(/An owner or an admin should check Nodes/)
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Revoke these nodes too" })
  ).not.toBeInTheDocument()
})

it("revokes a plain key without asking the server for nodes", async () => {
  const { calls, user, dialog } = await open({})
  await user.click(within(dialog).getByRole("button", { name: "Revoke" }))
  await vi.waitFor(() =>
    expect(calls.some((c) => c.method === "DELETE")).toBe(true)
  )
  expect(calls.find((c) => c.method === "DELETE")?.url).not.toContain(
    "compromised"
  )
})
