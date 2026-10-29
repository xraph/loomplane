import { expect, it } from "vitest"

import type { Binding, RouteOperation } from "@/client/forge/loomplane/types"
import { operationRecords, routablePorts } from "./routable-ports"

const binding = (provider: string, config: object | string): Binding =>
  ({
    provider,
    config: typeof config === "string" ? config : JSON.stringify(config),
  }) as Binding

const record = (name: string, value: string) => ({
  id: name,
  name,
  ttl: 300,
  type: "A",
  value,
  zoneId: "zone",
})

it("keeps the graph's ports for a server binding or no binding", () => {
  expect(routablePorts([80, 8080])).toEqual({ ports: [80, 8080] })
  expect(
    routablePorts([80, 8080], binding("server", { port: 9000, public: true }))
  ).toEqual({ ports: [80, 8080] })
})

it("offers only the port a public App Platform service listens on", () => {
  expect(
    routablePorts(
      [80, 8080],
      binding("digitalocean", { port: 8080, public: true })
    )
  ).toEqual({ ports: [8080] })
})

it("offers no port for a private App Platform service, and says why", () => {
  expect(
    routablePorts(
      [8080],
      binding("digitalocean", { port: 8080, public: false })
    )
  ).toEqual({
    ports: [],
    reason:
      "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first.",
  })
})

it("falls back to the graph's ports when the config can't be read", () => {
  expect(routablePorts([8080], binding("digitalocean", "not json"))).toEqual({
    ports: [8080],
  })
})

it("lists every record an operation wants, or the single one", () => {
  const a = record("@", "1.1.1.1")
  const b = record("@", "1.1.1.2")
  expect(
    operationRecords({
      desiredRecord: a,
      desiredRecords: [a, b],
    } as RouteOperation)
  ).toEqual([a, b])
  expect(operationRecords({ desiredRecord: a } as RouteOperation)).toEqual([a])
})
