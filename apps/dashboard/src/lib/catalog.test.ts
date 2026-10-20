import { expect, it } from "vitest"
import type { Plan } from "@/client/forge/loomplane/types"
import { assertDatabasePlan } from "@/lib/catalog"

const plan = {
  catalog: true,
  executable: true,
  status: "ready",
  projectId: "project",
  manifestId: "manifest",
  environment: "production",
  sources: [],
  problems: [],
  warnings: [{ code: "shared-host-storage" }],
  actions: [
    {
      op: "create",
      subject: "resource",
      provider: "server",
      name: "database",
      connectionId: "target",
      resource: {
        kind: "postgres",
        version: "17",
        target: "target",
        catalog: { image: "postgres@sha256:digest" },
      },
    },
  ],
} as unknown as Plan
it("approves only the single PostgreSQL creation the user requested", () => {
  expect(() =>
    assertDatabasePlan(plan, "project", "manifest", "target")
  ).not.toThrow()
  for (const changed of [
    {
      ...plan,
      actions: [...plan.actions, { ...plan.actions[0], op: "delete" }],
    },
    { ...plan, actions: [{ ...plan.actions[0], connectionId: "other" }] },
    { ...plan, manifestId: "changed" },
    { ...plan, warnings: [{ code: "new-guarantee" }] },
    { ...plan, sources: [{ id: "repo" }] },
    { ...plan, executable: false },
  ])
    expect(() =>
      assertDatabasePlan(changed as Plan, "project", "manifest", "target")
    ).toThrow()
})

it("approves only the selected MySQL or MariaDB recipe", () => {
  for (const [kind, version] of [
    ["mysql", "8.4"],
    ["mariadb", "11.8"],
  ] as const) {
    const selected = structuredClone(plan)
    selected.actions[0].resource!.kind = kind
    selected.actions[0].resource!.version = version
    expect(() =>
      assertDatabasePlan(selected, "project", "manifest", "target", kind)
    ).not.toThrow()
    selected.actions[0].resource!.kind = "postgres"
    expect(() =>
      assertDatabasePlan(selected, "project", "manifest", "target", kind)
    ).toThrow()
  }
})
