import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { Connection, Profile } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { NodeAdoptions } from "./node-adoptions"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const cloud = { id: "c", revision: 4, name: "Shared" } as Profile
const connections = [
  {
    id: "conn_node",
    name: "home-box",
    cloud: "server",
    method: "node",
    status: "ready",
    details: { node_id: "n_one" },
  },
] as unknown as Connection[]

const adoption = (over: Record<string, unknown>) => ({
  node_id: "n_one",
  node_name: "home-box",
  cloud_id: "c",
  status: "review",
  reason: "",
  server_id: "",
  operation_id: "",
  created_at: "2026-11-02T09:00:00Z",
  updated_at: "2026-11-02T09:00:00Z",
  ...over,
})

const writes: { path: string; body: Record<string, unknown> }[] = []
function fixture(adoptions: unknown[]) {
  writes.length = 0
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method && init.method !== "GET") {
        writes.push({ path, body: JSON.parse(String(init.body ?? "{}")) })
        return Response.json({
          id: "op",
          review: "immutable",
          status: "preview",
          input: {},
          actions: [],
          connection_generation: 3,
        })
      }
      if (path.endsWith("/node-adoptions")) return Response.json({ adoptions })
      return Response.json({})
    }
  )
}
const wrap = () =>
  render(
    <ClientProvider client={forgeClient}>
      <NodeAdoptions
        cloud={cloud}
        connections={connections}
        onChanged={() => {}}
      />
    </ClientProvider>
  )

it("lists a node that needs a review, with its reason, and starts the ordinary adopt review", async () => {
  fixture([adoption({ reason: "Docker 24 is older than Loomplane supports" })])
  const user = userEvent.setup()
  wrap()
  expect(await screen.findByText("home-box")).toBeInTheDocument()
  expect(
    screen.getByText("Docker 24 is older than Loomplane supports")
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Adopt home-box" }))

  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog).getByText("Connect a server you already run")
  ).toBeInTheDocument()
  await user.click(within(dialog).getByRole("checkbox"))
  await user.click(
    within(dialog).getByRole("button", { name: "Review server" })
  )
  await vi.waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]!.path).toBe("/api/v1/clouds/c/servers/preview")
  expect(writes[0]!.body).toMatchObject({
    kind: "adopt",
    cloud_revision: 4,
    connection_id: "conn_node",
    name: "home-box",
    confirm_adoption: true,
  })
})

it("shows a failed adoption and a node still on its way, and hides an applied one", async () => {
  fixture([
    adoption({ status: "failed", reason: "the node went offline" }),
    adoption({ node_id: "n_two", node_name: "lab-2", status: "waiting" }),
    adoption({ node_id: "n_three", node_name: "done-3", status: "applied" }),
  ])
  wrap()
  expect(await screen.findByText("the node went offline")).toBeInTheDocument()
  expect(screen.getByText("Waiting for the node")).toBeInTheDocument()
  expect(screen.queryByText("done-3")).toBeNull()
  // Only a node that needs a person gets the action.
  expect(screen.getAllByRole("button", { name: /^Adopt / })).toHaveLength(1)
})

it("shows nothing when no node needs attention", async () => {
  fixture([adoption({ status: "applied" })])
  const { container } = wrap()
  await vi.waitFor(() => expect(container).toBeEmptyDOMElement())
})
