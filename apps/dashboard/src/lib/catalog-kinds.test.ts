import { BoxIcon, DatabaseIcon, PlugIcon } from "lucide-react"
import { expect, test } from "vitest"

import type { Binding } from "@/client/forge/loomplane/types"
import {
  catalogKind,
  catalogKinds,
  catalogResources,
  defineCatalogKind,
  inCatalog,
  kindIcon,
  parseResourceConfig,
  serviceBindings,
} from "@/lib/catalog-kinds"

test("discovers kind modules from src/catalog-kinds", () => {
  expect(catalogKind("postgres")?.panels[0].id).toBe("recovery")
  expect(catalogKind("postgres")?.panels[0].title).toBe("Recovery")
  expect(catalogKinds().map((m) => m.kind)).toEqual(
    expect.arrayContaining(["postgres", "mysql", "mariadb", "redis", "valkey"])
  )
  expect(catalogKind("nope")).toBeUndefined()
})

test("Memcached advertises cache rebuilding without native recovery controls", () => {
  const module = catalogKind("memcached")
  expect(module?.group).toBe("cache")
  expect(module?.features).toContain("Rebuild from your app")
  expect(module?.blurb).toMatch(/No authentication/)
  expect(module?.panels).toEqual([])
  expect(inCatalog({ name: "memcached", placement: "app-project" })).toBe(true)
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

test("hidden modules and dependency-only kinds stay out of the catalog", () => {
  defineCatalogKind({
    kind: "tucked",
    title: "Tucked",
    group: "data",
    blurb: "Never in the catalog.",
    features: [],
    panels: [],
    hidden: true,
  })
  expect(inCatalog({ name: "tucked", placement: "app-project" })).toBe(false)
  expect(inCatalog({ name: "graph", placement: "dependency" })).toBe(false)
  expect(inCatalog({ name: "gateway", placement: "app-project" })).toBe(true)
  expect(inCatalog({ name: "postgres", placement: "service-project" })).toBe(
    true
  )
})

test("icon is used when there is no product logo", () => {
  const Layers = () => null
  const module = defineCatalogKind({
    kind: "layered",
    title: "Layered",
    group: "data",
    blurb: "Brings its own icon.",
    features: [],
    panels: [],
    icon: Layers,
  })
  expect(kindIcon(module)).toBe(Layers)
  // Without one, the group's icon; one icon for databases everywhere.
  expect(kindIcon(catalogKind("valkey"))).not.toBe(Layers)
  expect(kindIcon(catalogKind("postgres"))).toBe(DatabaseIcon)
  expect(kindIcon({ ...module, icon: undefined, group: "gateway" })).toBe(
    PlugIcon
  )
  expect(kindIcon(undefined)).toBe(BoxIcon)
})

test("a composed resource's parts are the bindings that say so, not every name it prefixes", () => {
  const resource = (name: string, config: object) =>
    ({ subject: "resource", name, config }) as unknown as Binding
  const component = (name: string, parent: string, kind = "sample") =>
    resource(name, {
      kind,
      provider: "server",
      component: {
        kind,
        resource: parent,
        protection: "none",
        component: { name: name.split("--").pop(), role: "process" },
      },
    })
  const bindings = [
    resource("app", {
      kind: "sample",
      provider: "server",
      composition: {
        kind: "sample",
        version: "1",
        dependencies: {
          postgres: {
            mode: "new",
            resource: "app--postgres",
            kind: "postgres",
          },
          graph: { mode: "new", resource: "app--graph", kind: "graph" },
          cache: { mode: "off" },
        },
      },
    }),
    component("app--api", "app"),
    component("app--worker", "app"),
    // A provisioned database and a dependency-only kind's one component,
    // which is its own resource.
    resource("app--postgres", { kind: "postgres", provider: "server" }),
    component("app--graph", "app--graph", "graph"),
    // A database someone named after app, and another composition's part.
    resource("app--reports", { kind: "postgres", provider: "server" }),
    component("app--other", "other"),
    { subject: "service", name: "app--web", config: {} } as unknown as Binding,
  ]
  expect(
    serviceBindings(bindings, "app").components.map((b) => b.name)
  ).toEqual(["app--api", "app--graph", "app--postgres", "app--worker"])
  expect(catalogResources(bindings).map((b) => b.name)).toEqual([
    "app",
    "app--reports",
    "app--other",
  ])
  expect(serviceBindings(bindings, "app--reports").components).toEqual([])
})

test("NATS exposes native recovery and describes file persistence", () => {
  const module = catalogKind("nats")
  expect(module?.title).toBe("NATS JetStream")
  expect(module?.features).toContain("Messages and consumers")
  expect(module?.panels[0].id).toBe("recovery")
})

test("RabbitMQ exposes message recovery and warns about backup downtime", () => {
  const module = catalogKind("rabbitmq")
  expect(module?.title).toBe("RabbitMQ")
  expect(module?.blurb).toMatch(/disconnect.*stop/i)
  expect(module?.features).toContain("Message backups")
  expect(module?.panels[0].id).toBe("recovery")
})
