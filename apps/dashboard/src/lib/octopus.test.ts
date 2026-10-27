import { expect, test } from "vitest"

import type {
  Board,
  BoardRoute,
  HostCard,
} from "@/client/forge/loomplane/types"
import {
  attachedHosts,
  discoveryState,
  discoverySentence,
  exampleRequest,
  farpCollision,
  farpHostnamesWith,
  farpSchemas,
  filterBoard,
  gatewayRouteInput,
  manualRoutes,
  methodsLabel,
  routeCountLabel,
  routeDraft,
  routeSourceLabel,
  splitPath,
  unroutedSentence,
  validatePath,
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

test("route sources and counts read like the mock", () => {
  expect(routeSourceLabel("farp")).toBe("FARP")
  expect(routeSourceLabel("here")).toBe("Added here")
  expect(routeSourceLabel("octopus-admin")).toBe("Octopus admin")
  expect(routeCountLabel(1)).toBe("1 route")
  expect(routeCountLabel(4)).toBe("4 routes")
})

test("routeDraft starts the dialog from a row without generated notes", () => {
  const farp = route({
    path: "/v1/orders/*",
    methods: ["GET", "POST"],
    note: "From the OpenAPI schema",
  })
  expect(routeDraft("override", "api.acme.dev", farp)).toEqual({
    hostname: "api.acme.dev",
    path: "/v1/orders/*",
    methods: ["GET", "POST"],
    service: "api",
    port: 8080,
    stripPrefix: false,
  })
  // The server writes the strip line when the route has no note of its own.
  const strip = route({
    source: "here",
    path: "/auth/*",
    service: "auth",
    port: 9000,
    stripPrefix: true,
    note: "Strips /auth before forwarding",
    routeId: "rt_1",
    revision: 3,
  })
  expect(routeDraft("edit", "api.acme.dev", strip)).toEqual({
    hostname: "api.acme.dev",
    path: "/auth/*",
    methods: [],
    service: "auth",
    port: 9000,
    stripPrefix: true,
    note: "",
    routeId: "rt_1",
    revision: 3,
  })
  expect(
    routeDraft("edit", "acme.dev", { ...strip, note: "Storefront" }).note
  ).toBe("Storefront")
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

test("exampleRequest follows the mock", () => {
  const auth = {
    host: "api.acme.dev",
    path: "/auth/*",
    strip: true,
    service: "auth",
    port: 9000,
    privateUrl: "http://edge--gateway:8080",
  }
  expect(exampleRequest(auth)).toEqual({
    in: "https://api.acme.dev/auth/login",
    out: "auth:9000/login",
  })
  expect(exampleRequest({ ...auth, strip: false })).toEqual({
    in: "https://api.acme.dev/auth/login",
    out: "auth:9000/auth/login",
  })
  expect(exampleRequest({ ...auth, host: "*.acme.dev" }).in).toBe(
    "https://shop1.acme.dev/auth/login"
  )
  expect(exampleRequest({ ...auth, host: "" }).in).toBe(
    "http://edge--gateway:8080/auth/login"
  )
  // An exact path strips to the root, and "/" and "/*" never strip.
  expect(exampleRequest({ ...auth, path: "/auth" })).toEqual({
    in: "https://api.acme.dev/auth",
    out: "auth:9000/",
  })
  expect(exampleRequest({ ...auth, path: "/*" })).toEqual({
    in: "https://api.acme.dev/login",
    out: "auth:9000/login",
  })
  expect(
    exampleRequest({ ...auth, path: "/", service: "web", port: 3000 })
  ).toEqual({ in: "https://api.acme.dev/", out: "web:3000/" })
})

test("farpCollision finds a FARP route on the same host and path", () => {
  const orders = route({ path: "/v1/orders/*", service: "api" })
  const mine = route({ path: "/auth/*", source: "here", service: "auth" })
  const b = board({
    hosts: [
      host("api.acme.dev", [orders, mine]),
      host("acme.dev", [route({ path: "/v1/events", service: "billing" })]),
    ],
  })
  expect(farpCollision(b, "api.acme.dev", "/v1/orders/*")).toBe(orders)
  expect(farpCollision(b, "api.acme.dev", "/v1/orders")).toBeUndefined()
  expect(farpCollision(b, "acme.dev", "/v1/orders/*")).toBeUndefined()
  // A route added here isn't FARP's.
  expect(farpCollision(b, "api.acme.dev", "/auth/*")).toBeUndefined()
  expect(farpCollision(b, "docs.acme.dev", "/v1/orders/*")).toBeUndefined()
})

test("validatePath says what the server says", () => {
  for (const ok of ["/", "/*", "/auth", "/auth/*", "/v1/orders/*"])
    expect(validatePath(ok)).toBeUndefined()
  expect(validatePath("")).toBe("A path starts with /, like /auth or /auth/*.")
  expect(validatePath("auth")).toBe(
    "A path starts with /, like /auth or /auth/*."
  )
  expect(validatePath("/" + "a".repeat(512))).toBe(
    "A path is at most 512 characters."
  )
  expect(validatePath("/a?b")).toBe(
    "A path can't contain ? or #. Query strings and fragments aren't part of a route."
  )
  expect(validatePath("/a b")).toBe(
    "A path can't contain spaces or control characters."
  )
  expect(validatePath("/a//b")).toBe("A path can't contain //.")
  expect(validatePath("/auth/")).toBe(
    "Drop the trailing slash, or end with /* to match everything under it."
  )
  expect(validatePath("/a/../b")).toBe("A path can't contain . or .. segments.")
  expect(validatePath("/a/*/b")).toBe(
    "A * is only allowed as the last segment, as /*."
  )
  expect(validatePath("/a*")).toBe(
    "A * is only allowed as the last segment, as /*."
  )
})

test("gatewayRouteInput always routes to port 8080 with the blocked paths", () => {
  const b = board()
  expect(
    gatewayRouteInput(
      { ...b.gateway.attach, port: 9090 },
      { zoneId: "zone_dev", hostname: "*.acme.dev" }
    )
  ).toEqual({
    zoneId: "zone_dev",
    cloudId: "",
    environmentId: "env_prod",
    service: "edge--gateway",
    hostname: "*.acme.dev",
    port: 8080,
    blockedPathPrefixes: ["/__farp", "/_farp", "/admin"],
  })
})

test("farpHostnamesWith keeps only attached hostnames", () => {
  const b = board({
    hosts: [
      host("", []),
      host("acme.dev", [route({ source: "here" }), route({})]),
      host("api.acme.dev", []),
      { ...host("old.acme.dev", []), attached: false },
    ],
  })
  b.gateway.farpHostnames = ["api.acme.dev", "old.acme.dev"]
  expect(attachedHosts(b).map((h) => h.hostname)).toEqual([
    "acme.dev",
    "api.acme.dev",
  ])
  expect(farpHostnamesWith(b, "acme.dev", true)).toEqual([
    "api.acme.dev",
    "acme.dev",
  ])
  expect(farpHostnamesWith(b, "api.acme.dev", false)).toEqual([])
  expect(manualRoutes(b.hosts[1])).toBe(1)
})

test("discovery states and schemas", () => {
  expect(discoveryState("registered")).toEqual({
    label: "Registered",
    tone: "overdye",
  })
  expect(discoveryState("discovered").tone).toBe("overdye")
  expect(discoveryState("waiting")).toEqual({
    label: "Waiting for a release",
    tone: "weld",
  })
  expect(discoveryState("failed")).toEqual({ label: "Failed", tone: "failed" })
  expect(
    farpSchemas({
      "farp.graphql.path": "/graphql",
      "farp.openapi.path": "/openapi.json",
      "farp.health.path": "/health",
      "farp.grpc.reflection": "true",
      "farp.port": "8080",
    })
  ).toEqual(["/openapi.json", "/graphql", "gRPC reflection"])
  expect(farpSchemas(undefined)).toEqual([])
})
