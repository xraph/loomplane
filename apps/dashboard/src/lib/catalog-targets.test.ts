import { expect, it } from "vitest"
import type { Connection, Plan } from "@/client/forge/loomplane/types"
import { databaseTargetProvider } from "@/lib/catalog-targets"
import { assertDatabasePlan } from "@/lib/catalog"

const target = {
  id: "cluster",
  cloud: "kubernetes",
  status: "ready",
  details: {
    architecture: "arm64",
    namespace: "databases",
    namespace_uid: "namespace-identity",
    storage_class: "retained",
    storage_class_uid: "class-identity",
    storage_reclaim: "Retain",
    persistent_storage: "ready",
    postgres_recovery_permissions: "ready",
  },
} as unknown as Connection

it("offers Kubernetes only for PostgreSQL with checked persistent recovery prerequisites", () => {
  expect(databaseTargetProvider(target, "postgres")).toBe("kubernetes")
  expect(databaseTargetProvider(target, "mysql")).toBeUndefined()
  expect(databaseTargetProvider(target, "mariadb")).toBeUndefined()
  for (const key of [
    "architecture",
    "namespace_uid",
    "storage_class_uid",
    "persistent_storage",
    "postgres_recovery_permissions",
  ]) {
    const changed = structuredClone(target)
    delete changed.details[key]
    expect(databaseTargetProvider(changed, "postgres")).toBeUndefined()
  }
  expect(
    databaseTargetProvider({ ...target, status: "incomplete" }, "postgres")
  ).toBeUndefined()
  expect(databaseTargetProvider({ ...target, cloud: "server" }, "mysql")).toBe(
    "server"
  )
})

const plan = {
  catalog: true,
  executable: true,
  status: "ready",
  projectId: "project",
  manifestId: "manifest",
  environment: "production",
  sources: [],
  problems: [],
  warnings: [{ code: "retained-cluster-storage" }],
  actions: [
    {
      op: "create",
      subject: "resource",
      provider: "kubernetes",
      name: "database",
      connectionId: "cluster",
      resource: {
        provider: "kubernetes",
        kind: "postgres",
        version: "17",
        target: "cluster",
        targetSettings: target.details,
        catalog: {
          id: "postgresql-17-kubernetes",
          revision: "1",
          productVersion: "17.11",
          architecture: "arm64",
          image:
            "docker.io/library/postgres@sha256:0b2882c46a2b8d8e148431b333394348cf4d7f15aec6df7f17d51dc641e56cfa",
          mountPath: "/var/lib/postgresql/data",
          backupFormat: "pg-dump-custom-v1",
          volumeSizeMb: 10240,
        },
      },
    },
  ],
} as unknown as Plan

it("limits the cluster Deploy action to its selected provider and complete persistence recipe", () => {
  expect(() =>
    assertDatabasePlan(
      plan,
      "project",
      "manifest",
      "cluster",
      "postgres",
      "kubernetes"
    )
  ).not.toThrow()
  for (const field of [
    "id",
    "revision",
    "productVersion",
    "image",
    "mountPath",
    "backupFormat",
    "architecture",
    "volumeSizeMb",
  ]) {
    const changed = structuredClone(plan) as unknown as {
      actions: { resource: { catalog: Record<string, unknown> } }[]
    }
    changed.actions[0].resource.catalog[field] = "changed"
    expect(() =>
      assertDatabasePlan(
        changed as unknown as Plan,
        "project",
        "manifest",
        "cluster",
        "postgres",
        "kubernetes"
      )
    ).toThrow()
  }
  expect(() =>
    assertDatabasePlan(
      plan,
      "project",
      "manifest",
      "cluster",
      "postgres",
      "server"
    )
  ).toThrow()
  expect(() =>
    assertDatabasePlan(
      plan,
      "project",
      "manifest",
      "cluster",
      "mysql",
      "kubernetes"
    )
  ).toThrow()
})

it("returns changed cluster placement and Cloud adoption actions to ordinary review", () => {
  for (const key of ["namespace_uid", "storage_class_uid", "architecture"]) {
    const changed = structuredClone(plan)
    changed.actions[0].resource!.targetSettings![key] = "replacement"
    expect(() =>
      assertDatabasePlan(
        changed,
        "project",
        "manifest",
        "cluster",
        "postgres",
        "kubernetes",
        target.details
      )
    ).toThrow()
  }
  const adopted = structuredClone(plan)
  adopted.actions[0].adoptCloudPlacement = true
  expect(() =>
    assertDatabasePlan(
      adopted,
      "project",
      "manifest",
      "cluster",
      "postgres",
      "kubernetes",
      target.details
    )
  ).toThrow()
})
