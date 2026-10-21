import { expect, it } from "vitest"
import type { Plan } from "@/client/forge/loomplane/types"
import { assertDatabasePlan } from "@/lib/catalog"
import { databaseTargetProvider } from "@/lib/catalog-targets"
import { managedDatabaseProfile } from "@/lib/managed-catalog"
import { managedDatabaseFixture } from "@/test-fixtures/managed-database"

for (const target of ["rds", "cloud-sql"] as const) {
  it(`offers ${target} only for a checked account and published PostgreSQL profile`, () => {
    const { connection, provider, profile } = managedDatabaseFixture(target)
    expect(databaseTargetProvider(connection, "postgres")).toBe(target)
    expect(databaseTargetProvider(connection, "mysql")).toBeUndefined()
    expect(
      databaseTargetProvider(
        { ...connection, status: "incomplete" },
        "postgres"
      )
    ).toBeUndefined()
    expect(
      databaseTargetProvider({ ...connection, accountIdentity: "" }, "postgres")
    ).toBeUndefined()
    expect(managedDatabaseProfile([provider], target)).toEqual(profile)
    expect(
      managedDatabaseProfile([{ ...provider, available: false }], target)
    ).toBeUndefined()
    expect(
      managedDatabaseProfile([{ ...provider, provisions: [] }], target)
    ).toBeUndefined()
  })

  it(`limits ${target} approval to the displayed profile, account and region`, () => {
    const { connection, provider, review } = managedDatabaseFixture(target)
    const plan = {
      id: "plan",
      revision: "reviewed",
      catalog: true,
      executable: true,
      status: "ready",
      projectId: "project",
      manifestId: "manifest",
      environment: "production",
      connections: { [provider.cloud]: connection.id },
      sources: [],
      problems: [],
      warnings: [],
      actions: [
        {
          op: "create",
          subject: "resource",
          provider: target,
          region: review.region,
          name: "database",
          resource: {
            kind: "postgres",
            provider: target,
            version: "17",
            region: review.region,
            managed: {
              profile: review.profile,
              connectionId: connection.id,
              accountIdentity: review.accountIdentity,
            },
          },
        },
      ],
    } as unknown as Plan
    const approve = (candidate: Plan) =>
      assertDatabasePlan(
        candidate,
        "project",
        "manifest",
        connection.id,
        "postgres",
        target,
        undefined,
        review
      )
    expect(() => approve(plan)).not.toThrow()
    for (const mutate of [
      (p: Plan) => {
        p.actions[0].region = "another-region"
      },
      (p: Plan) => {
        p.actions[0].resource!.region = "another-region"
      },
      (p: Plan) => {
        p.connections[provider.cloud] = "another-account"
      },
      (p: Plan) => {
        p.actions[0].resource!.managed!.accountIdentity = "changed"
      },
      (p: Plan) => {
        p.actions[0].resource!.managed!.connectionId = "changed"
      },
      (p: Plan) => {
        p.actions[0].resource!.target = "container"
      },
      (p: Plan) => {
        p.actions[0].resource!.catalog = { image: "unreviewed-image" } as never
      },
      (p: Plan) => {
        p.actions[0].resource!.managed = undefined
      },
      (p: Plan) => {
        p.warnings = [{ code: "additional-cost", message: "Review required" }]
      },
      (p: Plan) => {
        p.actions.push({ ...p.actions[0], op: "delete" })
      },
    ]) {
      const changed = structuredClone(plan)
      mutate(changed)
      expect(() => approve(changed)).toThrow()
    }
    for (const key of Object.keys(review.profile)) {
      const changed = structuredClone(plan)
      const profile = changed.actions[0].resource!.managed!
        .profile as unknown as Record<string, unknown>
      profile[key] =
        typeof profile[key] === "number"
          ? Number(profile[key]) + 1
          : typeof profile[key] === "boolean"
            ? !profile[key]
            : "changed"
      expect(() => approve(changed), key).toThrow()
    }
  })
}

it("rejects incomplete or malformed displayed profile metadata", () => {
  const { profile, provider } = managedDatabaseFixture("rds")
  const offered = (candidate: unknown) =>
    managedDatabaseProfile(
      [
        {
          ...provider,
          provisions: [
            {
              kind: "postgres",
              creates: "Managed database",
              managedProfiles: [candidate as typeof profile],
            },
          ],
        },
      ],
      "rds"
    )
  for (const key of [
    "id",
    "revision",
    "provider",
    "kind",
    "version",
    "computeClass",
    "storageType",
    "availability",
    "access",
    "backupRetentionUnit",
    "finalBackup",
  ])
    for (const value of [null, undefined, "", " ", true])
      expect(
        offered({ ...profile, [key]: value }),
        `${key}: ${String(value)}`
      ).toBeUndefined()
  for (const key of [
    "storageGb",
    "backupRetentionValue",
    "pointInTimeRecoveryDays",
    "finalBackupRetentionDays",
  ])
    for (const value of [null, undefined, Infinity, NaN, -1, 1.5, "7"])
      expect(
        offered({ ...profile, [key]: value }),
        `${key}: ${String(value)}`
      ).toBeUndefined()
  for (const key of ["storageEncrypted", "retainAutomatedBackups"])
    for (const value of [null, undefined, "false", 1])
      expect(
        offered({ ...profile, [key]: value }),
        `${key}: ${String(value)}`
      ).toBeUndefined()
  expect(offered(null)).toBeUndefined()
})
