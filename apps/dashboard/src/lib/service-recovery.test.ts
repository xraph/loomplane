import { expect, it } from "vitest"
import type { Backup, Binding, Restore } from "@/client/forge/loomplane/types"
import { recoverySelection } from "@/lib/service-recovery"

it("offers backup and scheduling for live Kubernetes PostgreSQL in the selected environment", () => {
  const binding = {
    name: "database",
    subject: "resource",
    provider: "kubernetes",
    environmentId: "production",
    config: {
      kind: "postgres",
      provider: "kubernetes",
      catalog: { id: "postgresql-17-kubernetes" },
    },
  } as unknown as Binding
  const selection = recoverySelection(
    [binding],
    [] as Backup[],
    [] as Restore[],
    "production",
    ""
  )
  expect(selection.resources).toEqual(["database"])
  expect(selection.live).toBe(true)
  expect(recoverySelection([binding], [], [], "preview", "").live).toBe(false)
  const unsupported = {
    ...binding,
    config: { kind: "mysql", catalog: { id: "mysql-8.4" } },
  } as unknown as Binding
  expect(recoverySelection([unsupported], [], [], "production", "").live).toBe(
    false
  )
})
