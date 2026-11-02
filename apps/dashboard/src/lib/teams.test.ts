import { expect, test } from "vitest"

import type { Team, TeamGrant } from "@/client/forge/loomplane/types"
import { grantLabel, teamRolesFor } from "./teams"

const names = (id: string) => (id === "proj_1" ? "checkout-api" : id)

function team(
  name: string,
  grants: Partial<TeamGrant>[],
  members = ["ausr_grace"]
): Team {
  return {
    id: `team_${name}`,
    orgId: "org",
    name,
    slug: name.toLowerCase(),
    description: "",
    createdAt: "",
    updatedAt: "",
    members: members.map((userId) => ({
      userId,
      email: "",
      name: "",
      addedAt: "",
    })),
    grants: grants.map((g, i) => ({
      id: `g${i}`,
      role: "viewer",
      projectId: "",
      ...g,
    })),
  }
}

test("shows only team roles above the member role", () => {
  const payments = team("Payments", [{ role: "admin", projectId: "proj_1" }])
  expect(teamRolesFor("ausr_grace", "viewer", [payments], names)).toEqual({
    teams: ["Payments"],
    higher: ["Admin on checkout-api through Payments"],
  })
  const devs = team("Devs", [{ role: "developer", projectId: "proj_1" }])
  expect(teamRolesFor("ausr_grace", "developer", [devs], names)).toEqual({
    teams: ["Devs"],
    higher: [],
  })
  const ops = team("Ops", [{ role: "developer", projectId: "" }])
  expect(teamRolesFor("ausr_grace", "viewer", [ops], names).higher).toEqual([
    "Developer on the workspace through Ops",
  ])
  // Someone else's team says nothing about Grace.
  expect(
    teamRolesFor("ausr_grace", "viewer", [team("Ops", [], ["ausr_bo"])], names)
  ).toEqual({ teams: [], higher: [] })
})

test("labels grants", () => {
  expect(grantLabel({ id: "g", role: "viewer", projectId: "" }, names)).toBe(
    "Viewer on the workspace"
  )
  expect(
    grantLabel({ id: "g", role: "developer", projectId: "proj_1" }, names)
  ).toBe("Developer on checkout-api")
})
