import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ProjectTeam } from "./project-team"

const access = vi.hoisted(() => ({ update: true, workspace: false }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => access.workspace }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.update = true
  access.workspace = false
})

const team = (id: string, name: string) => ({
  id,
  org_id: "org",
  name,
  slug: name.toLowerCase(),
  description: "",
  members: [],
  grants: [],
  created_at: "2026-10-30T10:00:00Z",
  updated_at: "2026-10-30T10:00:00Z",
})

function serve(responsible: string) {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "PUT") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json({ id: "proj_1", responsible_team_id: "" })
      }
      if (path === "/api/v1/teams")
        return Response.json({
          teams: [team("team_1", "Payments"), team("team_2", "Ops")],
        })
      if (path === "/api/v1/projects/proj_1/permissions")
        return Response.json({
          permissions: access.update ? ["project:update"] : [],
        })
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ProjectTeam
        project={{ id: "proj_1", responsibleTeamId: responsible } as Project}
      />
    </ClientProvider>
  )
  return writes
}

test("sets and clears the responsible team", async () => {
  const user = userEvent.setup()
  const writes = serve("")
  const select = await screen.findByRole("combobox", {
    name: "Responsible team",
  })
  expect(select).toHaveTextContent("None, workspace admins get its requests")
  expect(
    screen.getByText(
      "The agent sends this project's requests and notices to this team."
    )
  ).toBeInTheDocument()

  await waitFor(() => expect(select).toBeEnabled())
  await user.click(select)
  await user.click(await screen.findByRole("option", { name: "Payments" }))
  await waitFor(() =>
    expect(writes).toEqual([
      { path: "/api/v1/projects/proj_1/team", body: { team_id: "team_1" } },
    ])
  )
})

test("clears the team with the None option", async () => {
  const user = userEvent.setup()
  const writes = serve("team_1")
  const select = await screen.findByRole("combobox", {
    name: "Responsible team",
  })
  await waitFor(() => expect(select).toHaveTextContent("Payments"))
  await waitFor(() => expect(select).toBeEnabled())
  await user.click(select)
  await user.click(
    await screen.findByRole("option", {
      name: "None, workspace admins get its requests",
    })
  )
  await waitFor(() =>
    expect(writes).toEqual([
      { path: "/api/v1/projects/proj_1/team", body: { team_id: null } },
    ])
  )
})

test("is disabled without project update", async () => {
  // The workspace role may update projects, but this project's answer says no.
  access.workspace = true
  access.update = false
  serve("team_1")
  const select = await screen.findByRole("combobox", {
    name: "Responsible team",
  })
  await waitFor(() => expect(select).toBeDisabled())
  // The team's name arrives with the teams list, after the permissions.
  await waitFor(() => expect(select).toHaveTextContent("Payments"))
})
