import { expect, it } from "vitest"
import { assertDatabasePlan } from "@/lib/catalog"
import { managedDatabaseFixture } from "@/test-fixtures/managed-database"
import { managedDatabaseProfile } from "@/lib/managed-catalog"

const endpoint = {
  consumerProject: "project-a",
  network: "projects/project-a/global/networks/apps",
  forwardingRule:
    "projects/project-a/regions/us-central1/forwardingRules/database",
}

it("keeps the connector default even when PSC is listed first", () => {
  const { profile, provider } = managedDatabaseFixture("cloud-sql")
  const psc = {
    ...profile,
    id: "postgresql-17-cloud-sql-psc",
    access: "cloud-sql-psc-connector",
  }
  provider.provisions![0].managedProfiles = [psc, profile]
  expect(managedDatabaseProfile([provider], "cloud-sql")).toEqual(profile)
  expect(managedDatabaseProfile([provider], "cloud-sql", psc.id)).toEqual(psc)
  expect(
    managedDatabaseProfile([provider], "cloud-sql", "missing-profile")
  ).toBeUndefined()
})

it("refuses an unrequested PSC endpoint before automatic approval", () => {
  const { plan, connection, review } = managedDatabaseFixture("cloud-sql")
  plan.actions[0].resource!.managed!.cloudSqlPsc = endpoint
  expect(() =>
    assertDatabasePlan(
      plan,
      "project",
      "manifest",
      connection.id,
      "postgres",
      "cloud-sql",
      undefined,
      review
    )
  ).toThrow("The plan differs")
})

it("limits PSC approval to the exact requested manual endpoint", () => {
  const { plan, connection, review } = managedDatabaseFixture("cloud-sql")
  const profile = {
    ...review.profile,
    id: "postgresql-17-cloud-sql-psc",
    access: "cloud-sql-psc-connector",
  }
  const requested = { ...review, profile, cloudSqlPsc: endpoint }
  plan.actions[0].resource!.managed!.profile = profile
  plan.actions[0].resource!.managed!.cloudSqlPsc = { ...endpoint }
  const approve = () =>
    assertDatabasePlan(
      plan,
      "project",
      "manifest",
      connection.id,
      "postgres",
      "cloud-sql",
      undefined,
      requested
    )
  expect(approve).not.toThrow()
  for (const key of Object.keys(endpoint) as (keyof typeof endpoint)[]) {
    plan.actions[0].resource!.managed!.cloudSqlPsc = {
      ...endpoint,
      [key]: `${endpoint[key]}-changed`,
    }
    expect(approve, key).toThrow("The plan differs")
  }
  plan.actions[0].resource!.managed!.cloudSqlPsc = undefined
  expect(approve).toThrow("The plan differs")
})
