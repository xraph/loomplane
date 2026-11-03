import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Teams } from "./teams"

const access = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "ausr_ada" },
    can: (permission: string) =>
      permission === "member:manage" ? access.manage : true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.manage = true
})

type Write = { method: string; path: string; body: unknown }

const stamp = "2026-10-30T10:00:00Z"
const payments = {
  id: "team_1",
  org_id: "org",
  name: "Payments",
  slug: "payments",
  description: "Owns checkout",
  members: [
    {
      user_id: "ausr_bo",
      email: "bo@example.com",
      name: "Bo",
      added_at: stamp,
    },
  ],
  grants: [{ id: "grant_1", role: "developer", project_id: "proj_1" }],
  created_at: stamp,
  updated_at: stamp,
}

function serve(teams: unknown[] = [payments]) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      if (method !== "GET") {
        writes.push({
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        })
        return Response.json(payments)
      }
      switch (path) {
        case "/api/v1/teams":
          return Response.json({ teams })
        case "/api/v1/projects":
          return Response.json({
            projects: [
              { id: "proj_1", name: "checkout-api" },
              { id: "proj_2", name: "web" },
            ],
          })
        case "/api/v1/members":
          return Response.json({
            members: [
              member("amem_1", "ausr_ada", "Ada", "admin"),
              member("amem_2", "ausr_bo", "Bo", "viewer"),
              member("amem_3", "ausr_grace", "Grace", "viewer"),
            ],
          })
      }
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <Teams />
    </ClientProvider>
  )
  return writes
}

function member(id: string, userId: string, name: string, role: string) {
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

test("a viewer sees teams but can't change them", async () => {
  access.manage = false
  serve()
  const card = await screen.findByRole("region", { name: "Payments" })
  expect(within(card).getByText("Bo")).toBeInTheDocument()
  expect(
    await within(card).findByText("Developer on checkout-api")
  ).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: /create/i })).toBeNull()
  expect(screen.queryByRole("button", { name: /edit/i })).toBeNull()
  expect(screen.queryByRole("button", { name: /delete/i })).toBeNull()
  expect(screen.queryByRole("button", { name: /remove/i })).toBeNull()
  expect(screen.queryByRole("combobox")).toBeNull()
})

test("says so when there are no teams", async () => {
  serve([])
  expect(await screen.findByText("No teams yet")).toBeInTheDocument()
})

test("an older server's null list reads as no teams", async () => {
  serve(null as unknown as unknown[])
  expect(await screen.findByText("No teams yet")).toBeInTheDocument()
})

test("an admin creates a team, adds a member and grants a role", async () => {
  const user = userEvent.setup()
  const writes = serve()

  await user.click(await screen.findByRole("button", { name: "Create team" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Name"), "Platform")
  await user.click(within(dialog).getByRole("button", { name: "Create" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "POST",
      path: "/api/v1/teams",
      body: { name: "Platform" },
    })
  )

  const card = await screen.findByRole("region", { name: "Payments" })
  await user.click(
    await within(card).findByRole("combobox", {
      name: "Add a member to Payments",
    })
  )
  // Bo is in the team already, so only the others are offered.
  expect(screen.queryByRole("option", { name: "Bo" })).toBeNull()
  await user.click(await screen.findByRole("option", { name: "Grace" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "PUT",
      path: "/api/v1/teams/team_1/members/ausr_grace",
      body: undefined,
    })
  )

  await user.click(
    within(card).getByRole("combobox", { name: "Scope for Payments" })
  )
  await user.click(await screen.findByRole("option", { name: "web" }))
  await user.click(within(card).getByRole("button", { name: "Set role" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "PUT",
      path: "/api/v1/teams/team_1/grants",
      body: { role: "developer", project_id: "proj_2" },
    })
  )
})

test("whole workspace grants send no project", async () => {
  const user = userEvent.setup()
  const writes = serve()
  const card = await screen.findByRole("region", { name: "Payments" })
  await user.click(
    await within(card).findByRole("combobox", { name: "Role for Payments" })
  )
  await user.click(await screen.findByRole("option", { name: "Viewer" }))
  await user.click(within(card).getByRole("button", { name: "Set role" }))
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "PUT",
      path: "/api/v1/teams/team_1/grants",
      body: { role: "viewer" },
    })
  )
})

test("an admin removes a member and a role", async () => {
  const user = userEvent.setup()
  const writes = serve()
  const card = await screen.findByRole("region", { name: "Payments" })
  await user.click(
    await within(card).findByRole("button", { name: "Remove Bo from Payments" })
  )
  // The grant's label names the project, which the projects list fills in.
  await user.click(
    await within(card).findByRole("button", {
      name: "Remove Developer on checkout-api",
    })
  )
  await waitFor(() => {
    expect(writes).toContainEqual({
      method: "DELETE",
      path: "/api/v1/teams/team_1/members/ausr_bo",
      body: undefined,
    })
    expect(writes).toContainEqual({
      method: "DELETE",
      path: "/api/v1/teams/team_1/grants/grant_1",
      body: undefined,
    })
  })
})
