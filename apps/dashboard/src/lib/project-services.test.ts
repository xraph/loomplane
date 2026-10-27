import { describe, expect, it } from "vitest"

import type {
  Binding,
  CloudinfraRoute,
  Exclusion,
  Environment,
  Graph,
  Service,
} from "@/client/forge/loomplane/types"

import {
  callersOf,
  covering,
  deployedConfig,
  enabledIn,
  sizeLabel,
  filterRows,
  locationOf,
  matchesPath,
  serviceRows,
  stateOf,
  suggestedRule,
} from "./project-services"

const svc = (id: string, name: string, path: string): Service =>
  ({ id, name, path, entrypoint: path, kind: "http-service" }) as Service

const graph = {
  services: [
    svc("svc:a", "loomplane", "cmd/loomplane"),
    svc("svc:b", "loomplane-worker", "cmd/loomplane-worker"),
    svc("svc:c", "api", "tests/acceptance/fixture/cmd/api"),
  ],
  resources: [{ id: "res:pg", kind: "postgres" }],
  edges: [
    { from: "svc:a", to: "res:pg", kind: "needs" },
    { from: "svc:b", to: "res:pg", kind: "needs" },
  ],
} as unknown as Graph

const envs = [
  { id: "env_p", name: "production" },
  { id: "env_v", name: "preview" },
] as Environment[]

const bind = (name: string, extra: Partial<Binding> = {}): Binding =>
  ({
    id: `bnd_${name}`,
    name,
    subject: "service",
    url: "",
    drift: "",
    ...extra,
  }) as Binding

describe("serviceRows", () => {
  const rows = serviceRows(
    graph,
    envs,
    {
      production: [
        bind("loomplane", {
          url: "https://loomplane-production.ondigitalocean.app",
        }),
        bind("loomplane-worker", { drift: "changed" }),
        { ...bind("db"), subject: "resource" },
      ],
    },
    [
      {
        input: { service: "loomplane", hostname: "console.acme.dev" },
      } as CloudinfraRoute,
    ]
  )

  it("joins what runs in each environment by name", () => {
    expect(rows[0].deployed.production?.id).toBe("bnd_loomplane")
    expect(rows[0].deployed.preview).toBeUndefined()
    expect(stateOf(rows[1].deployed.production)).toBe("drifted")
    expect(stateOf(rows[2].deployed.production)).toBe("none")
  })

  it("lists routed hostnames before generated ones", () => {
    expect(rows[0].domains).toEqual([
      "console.acme.dev",
      "loomplane-production.ondigitalocean.app",
    ])
    expect(rows[1].domains).toEqual([])
  })

  it("names what each service needs", () => {
    expect(rows[0].uses).toEqual(["postgres"])
    expect(rows[2].uses).toEqual([])
  })

  it("filters by deployment and search", () => {
    expect(filterRows(rows, "deployed", "").map((r) => r.service.name)).toEqual(
      ["loomplane", "loomplane-worker"]
    )
    expect(filterRows(rows, "pending", "").map((r) => r.service.name)).toEqual([
      "api",
    ])
    expect(
      filterRows(rows, "all", "acceptance").map((r) => r.service.name)
    ).toEqual(["api"])
  })
})

describe("matchesPath", () => {
  it.each([
    ["tests/acceptance/**", "tests/acceptance/fixture/cmd/api", true],
    ["tests/acceptance/**", "tests/acceptancex/api", false],
    ["deployments/miget", "deployments/miget/build", true],
    ["cmd/*-worker", "cmd/loomplane-worker", true],
    ["cmd/*-worker", "cmd/loomplane", false],
    ["", "cmd/api", false],
  ])("%s against %s is %s", (pattern, name, want) => {
    expect(matchesPath(pattern, name)).toBe(want)
  })
})

describe("suggestedRule", () => {
  it("takes the directory several services share", () => {
    expect(
      suggestedRule([
        svc("1", "api", "tests/acceptance/fixture/cmd/api"),
        svc("2", "w", "tests/acceptance/fixture/cmd/worker"),
      ])
    ).toBe("tests/acceptance/fixture/cmd/**")
  })

  it("takes two directories above one deep service", () => {
    expect(
      suggestedRule([svc("1", "api", "tests/acceptance/fixture/cmd/api")])
    ).toBe("tests/acceptance/**")
  })

  it("proposes nothing for top-level or cmd services", () => {
    expect(suggestedRule([svc("1", "x", "cmd/loomctl")])).toBeUndefined()
    expect(
      suggestedRule([svc("1", "x", "cmd/a"), svc("2", "y", "cmd/b")])
    ).toBeUndefined()
  })
})

describe("locationOf", () => {
  it("names a root-module service by its entrypoint", () => {
    expect(
      locationOf({ path: ".", entrypoint: "cmd/loomctl" } as Service)
    ).toBe("cmd/loomctl")
    expect(
      locationOf({
        path: "apps/dashboard",
        entrypoint: "apps/dashboard",
      } as Service)
    ).toBe("apps/dashboard")
  })

  it("lets a root-module service suggest no rule of its own", () => {
    expect(
      suggestedRule([{ path: ".", entrypoint: "cmd/loomctl" } as Service])
    ).toBeUndefined()
  })
})

describe("covering", () => {
  const api = {
    id: "svc:api",
    path: "tests/acceptance/fixture/cmd/api",
    entrypoint: "tests/acceptance/fixture/cmd/api",
  } as Service
  const exclusions = [
    { kind: "service", match: "svc:api" },
    { kind: "path", match: "tests/acceptance/**" },
    { kind: "path", match: "docs/**" },
    { kind: "service", match: "svc:build" },
  ] as Exclusion[]

  it("finds the service's own exclusion and every path rule over it", () => {
    expect(covering([api], exclusions)).toEqual([
      { kind: "service", match: "svc:api" },
      { kind: "path", match: "tests/acceptance/**" },
    ])
  })
})

describe("deployedConfig", () => {
  it("reads links, size and health from a binding's config", () => {
    const c = deployedConfig({
      config: {
        env: [
          { key: "DATABASE_URL", from: "db", subject: "resource" },
          { bad: 1 },
        ],
        port: 8780,
        size: { cpu: 1, memory: 1024 },
        health_path: "/_/health",
        framework: "forge",
      },
    } as unknown as Binding)
    expect(c.env).toEqual([
      { key: "DATABASE_URL", from: "db", subject: "resource" },
    ])
    expect(c.port).toBe(8780)
    expect(sizeLabel(c)).toBe("1 vCPU, 1 GB")
    expect(c.healthPath).toBe("/_/health")
  })

  it("reads a JSON string and survives junk", () => {
    expect(
      deployedConfig({
        config: '{"kind":"postgres","version":"17"}',
      } as Binding)
    ).toMatchObject({ kind: "postgres", version: "17" })
    expect(deployedConfig({ config: "{nope" } as Binding).env).toEqual([])
    expect(deployedConfig(undefined).env).toEqual([])
  })
})

describe("callersOf", () => {
  it("lists deployed services linked to a service", () => {
    const worker = {
      name: "loomplane-worker",
      subject: "service",
      config: {
        env: [{ key: "LOOMPLANE_URL", from: "loomplane", subject: "service" }],
      },
    } as unknown as Binding
    const self = {
      name: "loomplane",
      subject: "service",
      config: {},
    } as unknown as Binding
    const db = {
      name: "db",
      subject: "resource",
      config: {},
    } as unknown as Binding
    expect(callersOf("loomplane", [worker, self, db])).toEqual([
      { binding: worker, key: "LOOMPLANE_URL" },
    ])
  })
})

describe("enabledIn", () => {
  it("is on unless the environment's override turns it off", () => {
    const svc = {
      source: "cmd/x",
      environments: { preview: { enabled: false }, staging: {} },
    }
    expect(enabledIn(svc, "preview")).toBe(false)
    expect(enabledIn(svc, "staging")).toBe(true)
    expect(enabledIn(svc, "production")).toBe(true)
    expect(enabledIn(undefined, "production")).toBe(true)
  })
})
