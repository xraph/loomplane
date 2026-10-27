import { expect, test } from "vitest"

import type {
  Board,
  BoardRoute,
  HostCard,
} from "@/client/forge/loomplane/types"
import {
  discoverySentence,
  filterBoard,
  methodsLabel,
  routeCountLabel,
  routeNote,
  routeSourceLabel,
  splitPath,
  unroutedSentence,
} from "@/lib/octopus"

const route = (over: Partial<BoardRoute>): BoardRoute => ({
  key: over.path ?? "/",
  source: "farp",
  path: "/",
  methods: [],
  service: "api",
  port: 8080,
  note: "",
  stripPrefix: false,
  overrides: false,
  ...over,
})

const host = (hostname: string, routes: BoardRoute[]): HostCard => ({
  hostname,
  attached: hostname !== "",
  farpHome: false,
  routes,
})

const board = (over: Partial<Board> = {}): Board => ({
  gateway: {
    resource: "edge",
    provider: "server",
    farpHostnames: ["api.acme.dev"],
    generation: 1,
    appliedGeneration: 1,
    revision: 1,
    syncedAt: "2026-10-26T09:00:00Z",
    attach: {
      environmentId: "env_prod",
      service: "edge--gateway",
      port: 8080,
      blockedPathPrefixes: ["/__farp", "/_farp", "/admin"],
    },
  },
  discovery: {
    mode: "push",
    lastChange: "2026-10-26T09:00:00Z",
    services: ["api", "billing", "auth"].map((service) => ({
      service,
      optedIn: true,
      state: "registered",
      routes: 1,
      updatedAt: "2026-10-26T09:00:00Z",
    })),
  },
  hosts: [],
  unrouted: [],
  unroutable: [],
  counts: { all: 0, farp: 0, here: 0 },
  live: true,
  ...over,
})

test("splitPath and methodsLabel", () => {
  expect(splitPath("/auth/*")).toEqual({ prefix: "/auth", wildcard: true })
  expect(splitPath("/*")).toEqual({ prefix: "/", wildcard: true })
  expect(splitPath("/v1/events")).toEqual({
    prefix: "/v1/events",
    wildcard: false,
  })
  expect(methodsLabel([])).toBe("Any")
  expect(methodsLabel(["GET", "POST"])).toBe("GET POST")
})

test("route sources, notes and counts read like the mock", () => {
  expect(routeSourceLabel("farp")).toBe("FARP")
  expect(routeSourceLabel("here")).toBe("Added here")
  expect(routeSourceLabel("octopus-admin")).toBe("Octopus admin")
  expect(routeCountLabel(1)).toBe("1 route")
  expect(routeCountLabel(4)).toBe("4 routes")
  expect(
    routeNote(route({ path: "/auth/*", source: "here", stripPrefix: true }))
  ).toBe("Strips /auth before forwarding")
  expect(
    routeNote(route({ note: "Storefront", path: "/shop/*", stripPrefix: true }))
  ).toBe("Storefront · Strips /shop before forwarding")
  expect(routeNote(route({ note: "From the OpenAPI schema" }))).toBe(
    "From the OpenAPI schema"
  )
})

test("filterBoard keeps the source's rows and drops empty cards", () => {
  const b = board({
    hosts: [
      host("acme.dev", [route({ source: "here" })]),
      host("api.acme.dev", [
        route({ path: "/v1/orders/*" }),
        route({ path: "/auth/*", source: "here" }),
        route({ path: "/debug", source: "octopus-admin" }),
      ]),
      // FARP routes answer on any host, but none are published yet.
      host("", []),
    ],
  })
  expect(
    filterBoard(b, "all").map((h) => [h.hostname, h.routes.length])
  ).toEqual([
    ["acme.dev", 1],
    ["api.acme.dev", 3],
  ])
  expect(
    filterBoard(b, "farp").map((h) => [h.hostname, h.routes.length])
  ).toEqual([["api.acme.dev", 1]])
  expect(
    filterBoard(b, "here").map((h) => [h.hostname, h.routes.length])
  ).toEqual([
    ["acme.dev", 1],
    ["api.acme.dev", 1],
  ])
})

test("discoverySentence covers landing hosts, any host and no services", () => {
  expect(discoverySentence(board(), "acme-shop", "production")).toEqual({
    lead: "FARP is watching acme-shop / production. 3 services publish schemas, and their routes land on ",
    hosts: ["api.acme.dev"],
    tail: ".",
  })
  const anyHost = board()
  anyHost.gateway.farpHostnames = []
  anyHost.discovery.services = anyHost.discovery.services.slice(0, 1)
  expect(discoverySentence(anyHost, "acme-shop", "production")).toEqual({
    lead: "FARP is watching acme-shop / production. 1 service publishes schemas, and their routes answer on every hostname until you pick where they land.",
    hosts: [],
    tail: "",
  })
  const none = board()
  none.discovery.services = [{ ...none.discovery.services[0], optedIn: false }]
  expect(discoverySentence(none, "acme-shop", "production")).toEqual({
    lead: "FARP is watching acme-shop / production. No service publishes FARP schemas yet. Add a farp block to a service in the manifest to have Octopus discover its routes.",
    hosts: [],
    tail: "",
  })
})

test("unroutedSentence names each service and why", () => {
  expect(
    unroutedSentence(
      board({
        unrouted: [{ service: "admin", port: 4000 }],
        unroutable: [
          { service: "worker", reason: "no-port" },
          { service: "batch", reason: "other-target" },
        ],
      })
    )
  ).toBe(
    "admin :4000 has no route yet. worker has no HTTP port, so it can't take one. batch runs on a different server or cluster, so Octopus can't reach it."
  )
  expect(unroutedSentence(board())).toBe("")
})
