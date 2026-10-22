import { expect, test } from "vitest"

import type { Binding } from "@/client/forge/loomplane/types"
import {
  catalogKind,
  catalogKinds,
  defineCatalogKind,
  parseResourceConfig,
} from "@/lib/catalog-kinds"

test("discovers kind modules from src/catalog-kinds", () => {
  expect(catalogKind("postgres")?.panels[0].id).toBe("recovery")
  expect(catalogKind("postgres")?.panels[0].title).toBe("Recovery")
  expect(catalogKinds().map((m) => m.kind)).toEqual(
    expect.arrayContaining(["postgres", "mysql", "mariadb", "redis", "valkey"])
  )
  expect(catalogKind("nope")).toBeUndefined()
})

test("rejects two modules registering one kind", () => {
  expect(() =>
    defineCatalogKind({
      kind: "postgres",
      title: "PostgreSQL",
      group: "database",
      blurb: "A second PostgreSQL.",
      features: [],
      panels: [],
    })
  ).toThrow("postgres is registered twice")
  expect(catalogKind("postgres")?.panels[0].id).toBe("recovery")
})

test("rejects a module with two panels of one id", () => {
  expect(() =>
    defineCatalogKind({
      kind: "twice",
      title: "Twice",
      group: "data",
      blurb: "Two panels with one id.",
      features: [],
      panels: [
        { id: "routes", title: "Routes", component: () => null },
        { id: "routes", title: "More routes", component: () => null },
      ],
    })
  ).toThrow("twice has two panels called routes")
  expect(catalogKind("twice")).toBeUndefined()
})

test("reads a binding's config whether it arrives as text or JSON", () => {
  const config = {
    kind: "sample",
    provider: "server",
    composition: {
      kind: "sample",
      version: "1",
      dependencies: { postgres: { mode: "external", external: "ep_1" } },
    },
  }
  const binding = (value: unknown) => ({ config: value }) as unknown as Binding
  expect(parseResourceConfig(binding(config))).toEqual(config)
  expect(parseResourceConfig(binding(JSON.stringify(config)))).toEqual(config)
  expect(parseResourceConfig(binding("not json"))).toBeUndefined()
  expect(parseResourceConfig(binding({ provider: "server" }))).toBeUndefined()
})
