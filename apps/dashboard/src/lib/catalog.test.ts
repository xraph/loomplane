import { expect, it } from "vitest"
import type { Plan } from "@/client/forge/loomplane/types"
import { assertPostgresPlan } from "@/lib/catalog"

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
    assertPostgresPlan(plan, "project", "manifest", "target")
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
      assertPostgresPlan(changed as Plan, "project", "manifest", "target")
    ).toThrow()
})
