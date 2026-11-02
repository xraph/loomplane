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

it("offers no port for an App Platform static site, and says why", () => {
  for (const config of [
    { execution: "static", workload: "static-site", public: true },
    { execution: "static", public: false, port: 8080 },
  ])
    expect(routablePorts([8080], binding("digitalocean", config))).toEqual({
      ports: [],
      reason: "Hostnames on App Platform static sites aren't supported yet.",
    })
  expect(
    routablePorts(
      [8080],
      binding("digitalocean", {
        execution: "container",
        public: true,
        port: 8080,
      })
    )
  ).toEqual({ ports: [8080] })
})

it("blocks an App Platform binding whose config can't be read", () => {
  for (const config of ["not json", "null", "", "5", "[]", '"public"'])
    expect(routablePorts([8080], binding("digitalocean", config))).toEqual({
      ports: [],
      reason:
        "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first.",
    })
})

it("takes a port only as a positive integer or digits", () => {
  for (const port of [true, [8080], " 8080 ", "+80", "-80", "8e3", 80.5])
    expect(
      routablePorts([8080], binding("digitalocean", { public: true, port }))
        .ports
    ).toEqual([])
  expect(
    routablePorts([1], binding("digitalocean", { public: true, port: "8080" }))
      .ports
  ).toEqual([8080])
})

it("offers no port when a public App Platform service has none", () => {
  const reason =
    "This service has no public port on App Platform. Set port in the manifest and deploy it first."
  for (const config of [
    { public: true },
    { public: true, port: 0 },
    { public: true, port: "web" },
  ])
    expect(routablePorts([8080], binding("digitalocean", config))).toEqual({
      ports: [],
      reason,
    })
})

it("reads public and port as the backend does, as text", () => {
  expect(
    routablePorts([80], binding("digitalocean", { public: "false", port: 80 }))
      .ports
  ).toEqual([])
  expect(
    routablePorts(
      [80],
      binding("digitalocean", { public: "true", port: "8080" })
    )
  ).toEqual({ ports: [8080] })
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
