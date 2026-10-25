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
