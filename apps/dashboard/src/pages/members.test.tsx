import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Members } from "./members"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "ausr_ada" },
    can: () => false,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const stamp = "2026-10-30T10:00:00Z"

function person(id: string, userId: string, name: string, role: string) {
  return {
    id,
    user_id: userId,
    email: `${name.toLowerCase()}@example.com`,
    name,
    role,
    member_role: role,
    joined_at: stamp,
  }
}

test("a member row shows team chips and higher team roles", async () => {
  const requests: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    requests.push(path)
    switch (path) {
      case "/api/v1/members":
        return Response.json({
          members: [
            person("amem_1", "ausr_ada", "Ada", "admin"),
            person("amem_2", "ausr_grace", "Grace", "viewer"),
          ],
        })
      case "/api/v1/teams":
        return Response.json({
          teams: [
            {
              id: "team_1",
              org_id: "org",
              name: "Payments",
              slug: "payments",
              description: "",
              members: [
                {
                  user_id: "ausr_grace",
                  email: "grace@example.com",
                  name: "Grace",
                  added_at: stamp,
                },
              ],
              grants: [
                { id: "g1", role: "admin", project_id: "proj_1" },
                { id: "g2", role: "viewer", project_id: "" },
              ],
              created_at: stamp,
              updated_at: stamp,
            },
          ],
        })
      case "/api/v1/projects":
        return Response.json({
          projects: [{ id: "proj_1", name: "checkout-api" }],
        })
    }
    return Response.json({ message: `unexpected ${path}` }, { status: 404 })
  })
  render(
    <ClientProvider client={forgeClient}>
      <Members />
    </ClientProvider>
  )

  const grace = (await screen.findByText("Grace")).closest("tr")!
  expect(await within(grace).findByText("Payments")).toBeInTheDocument()
  expect(within(grace).getByText("Viewer")).toBeInTheDocument()
  // Only the grant above her own role is spelled out.
  expect(
    await within(grace).findByText("Admin on checkout-api through Payments")
  ).toBeInTheDocument()
  expect(within(grace).queryByText(/Viewer on the workspace/)).toBeNull()

  const ada = screen.getByText("Ada").closest("tr")!
  expect(within(ada).queryByText("Payments")).toBeNull()
  // One read of the teams for the whole table.
  expect(requests.filter((p) => p === "/api/v1/teams")).toHaveLength(1)
})
