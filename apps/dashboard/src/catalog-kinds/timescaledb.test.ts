import { expect, test } from "vitest"

import { catalogKind } from "@/lib/catalog-kinds"

test("timescaledb is a database with recovery", () => {
  const module = catalogKind("timescaledb")
  expect(module?.group).toBe("database")
  expect(module?.logo).toBe("postgresql")
  expect(module?.panels.map((p) => p.id)).toEqual(["recovery"])
})

test("fabriq brings its own add dialog and icon", () => {
  const module = catalogKind("fabriq")
  expect(module?.addDialog).toBeDefined()
  expect(module?.icon).toBeDefined()
})

test("the standalone TimescaleDB shows Recovery on a server, not on Kubernetes", () => {
  const recovery = catalogKind("timescaledb")!.panels[0]
  const props = (provider: string) =>
    ({
      binding: {
        subject: "resource",
        provider,
        config: {
          kind: "timescaledb",
          provider,
          catalog: { id: `timescaledb-${provider}` },
        },
      },
    }) as unknown as Parameters<NonNullable<typeof recovery.when>>[0]
  expect(recovery.when?.(props("server"))).toBe(true)
  expect(recovery.when?.(props("kubernetes"))).toBe(false)
})
