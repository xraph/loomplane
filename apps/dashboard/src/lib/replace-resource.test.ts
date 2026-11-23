import { expect, it } from "vitest"
import type { Manifest, ProviderResource } from "@/client/forge/loomplane/types"
import {
  compatibleResourceKind,
  existingResourceId,
  replaceResource,
  resourceConsumers,
  resourceEnabledIn,
  updateResourceManifest,
} from "@/lib/replace-resource"

const workspaceResource = (
  overrides: Partial<ProviderResource> = {}
): ProviderResource =>
  ({
    id: "providerresource_shared_pg",
    name: "shared-pg",
    kind: "postgres",
    provider: "digitalocean",
    region: "nyc",
    endpointId: "endpoint_1",
    connectionState: "ready",
    observationState: "observed",
    observedAt: new Date().toISOString(),
    ...overrides,
  }) as ProviderResource

it("reads existing_resource in snake or camel case and checks kind compatibility", () => {
  expect(existingResourceId({ existing_resource: "r1" })).toBe("r1")
  expect(existingResourceId({ existingResource: "r2" })).toBe("r2")
  expect(existingResourceId({ existing_resource: "" })).toBeUndefined()
  expect(existingResourceId(undefined)).toBeUndefined()

  expect(compatibleResourceKind("valkey", "redis")).toBe(true)
  expect(compatibleResourceKind("redis", "valkey")).toBe(true)
  expect(compatibleResourceKind("postgres", "mysql")).toBe(false)
})

it("evaluates resourceEnabledIn with opt-in semantics for existing_resource blocks", () => {
  expect(resourceEnabledIn({ kind: "postgres" }, "production")).toBe(true)
  expect(
    resourceEnabledIn(
      { kind: "postgres", environments: { staging: { enabled: false } } },
      "production"
    )
  ).toBe(true)
  expect(
    resourceEnabledIn(
      { kind: "postgres", existing_resource: "r1" },
      "production"
    )
  ).toBe(true)
  expect(
    resourceEnabledIn(
      {
        kind: "postgres",
        existing_resource: "r1",
        environments: { production: { enabled: true } },
      },
      "staging"
    )
  ).toBe(false)
  expect(
    resourceEnabledIn(
      {
        kind: "postgres",
        existing_resource: "r1",
        environments: { production: { enabled: true } },
      },
      "production"
    )
  ).toBe(true)
})

it("replaces a managed resource with another project resource and merges options", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: {
        source: "cmd/api",
        needs: ["postgres", "postgres-2"],
        need_options: { postgres: { schema: "app" } },
      },
      worker: {
        source: "cmd/worker",
        needs: ["postgres"],
        need_options: { postgres: { role: "worker" } },
      },
    },
    resources: {
      postgres: { kind: "postgres", provider: "digitalocean" },
      "postgres-2": { kind: "postgres", provider: "digitalocean" },
      analytics: { kind: "sample", uses: { db: { resource: "postgres" } } },
    },
  }

  expect(resourceConsumers(m, "postgres")).toEqual({
    services: ["api", "worker"],
    resources: ["analytics"],
  })

  const next = replaceResource(m, "postgres", "postgres-2")
  expect(next.resources?.postgres).toBeUndefined()
  expect(next.resources?.analytics.uses.db.resource).toBe("postgres-2")
  expect(next.services?.api.needs).toEqual(["postgres-2"])
  expect(next.services?.api.need_options).toEqual({
    "postgres-2": { schema: "app" },
  })
  expect(next.services?.worker.needs).toEqual(["postgres-2"])
  expect(next.services?.worker.need_options).toEqual({
    "postgres-2": { role: "worker" },
  })
})

it("rejects replacing a resource when need_options conflict", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: {
        source: "cmd/api",
        needs: ["postgres", "postgres-2"],
        need_options: {
          postgres: { schema: "one" },
          "postgres-2": { schema: "two" },
        },
      },
    },
    resources: {
      postgres: { kind: "postgres" },
      "postgres-2": { kind: "postgres" },
    },
  }
  expect(() => replaceResource(m, "postgres", "postgres-2")).toThrow(
    "Resolve the conflicting schema dependency option for api before switching resources."
  )
})

it("replaces a managed resource with a workspace resource using safe names and environment opt-in", () => {
  const longName = "very-long-database-name-a-suffix"
  const m: Manifest = {
    version: 1,
    services: {
      api: { source: "cmd/api", needs: [longName] },
    },
    resources: {
      [longName]: {
        kind: "postgres",
        provider: "digitalocean",
        environments: { staging: { enabled: false } },
      },
      "very-long-database-name-a-existing": { kind: "postgres" },
    },
  }

  const next = replaceResource(m, longName, workspaceResource(), {
    environments: ["staging", "production"],
  })
  const createdName = Object.keys(next.resources ?? {}).find((n) =>
    n.startsWith("very-long-database-name-a-existing-")
  )
  expect(createdName).toBe("very-long-database-name-a-existing-2")
  expect(createdName?.includes("--")).toBe(false)
  expect(createdName!.length).toBeLessThanOrEqual(40)
  expect(next.resources?.[createdName!]).toEqual({
    kind: "postgres",
    existing_resource: "providerresource_shared_pg",
    environments: {
      staging: { enabled: false },
      production: { enabled: true },
    },
  })
  expect(next.services?.api.needs).toEqual([createdName])

  // When enabled in all environments, omit environments on the existing_resource.
  const allEnabled = replaceResource(
    {
      version: 1,
      services: { api: { source: "cmd/api", needs: ["postgres"] } },
      resources: { postgres: { kind: "postgres", provider: "digitalocean" } },
    },
    "postgres",
    workspaceResource(),
    { environments: ["staging", "production"] }
  )
  expect(allEnabled.resources?.["postgres-existing"]).toEqual({
    kind: "postgres",
    existing_resource: "providerresource_shared_pg",
  })
})

it("updates an existing_resource attachment in place without appending -existing again", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: { source: "cmd/api", needs: ["postgres-existing"] },
    },
    resources: {
      "postgres-existing": {
        kind: "postgres",
        existing_resource: "providerresource_old",
      },
    },
  }
  const next = replaceResource(
    m,
    "postgres-existing",
    workspaceResource({ id: "providerresource_new" })
  )
  expect(Object.keys(next.resources ?? {})).toEqual(["postgres-existing"])
  expect(next.resources?.["postgres-existing"]).toEqual({
    kind: "postgres",
    existing_resource: "providerresource_new",
  })
  expect(next.services?.api.needs).toEqual(["postgres-existing"])
})

it("removes a resource, unlinks services, blocks on resource consumers, and ignores last kind on request", () => {
  const blocked: Manifest = {
    version: 1,
    resources: {
      db: { kind: "postgres" },
      app: { kind: "sample", uses: { postgres: { resource: "db" } } },
    },
  }
  expect(() => replaceResource(blocked, "db")).toThrow(
    "Choose a replacement for app first"
  )

  const twoSameKind: Manifest = {
    version: 1,
    services: {
      api: {
        source: "cmd/api",
        needs: ["postgres"],
        need_options: { postgres: { schema: "app" } },
      },
    },
    resources: {
      postgres: { kind: "postgres" },
      "postgres-2": { kind: "postgres" },
    },
  }
  const afterTwo = replaceResource(twoSameKind, "postgres", undefined, {
    ignoreKind: true,
  })
  expect(afterTwo.resources?.postgres).toBeUndefined()
  expect(afterTwo.services?.api.needs).toBeUndefined()
  expect(afterTwo.services?.api.need_options).toBeUndefined()
  expect(afterTwo.ignore).toBeUndefined()

  const last = replaceResource(afterTwo, "postgres-2", undefined, {
    ignoreKind: true,
  })
  expect(last.resources).toBeUndefined()
  expect(last.ignore?.resources).toEqual(["postgres"])
})

it("updates version, settings, consumer wiring, and existing_resource environment toggles", () => {
  const m: Manifest = {
    version: 1,
    services: {
      api: {
        source: "cmd/api",
        needs: ["postgres"],
        need_options: { postgres: { schema: "app" } },
      },
      worker: { source: "cmd/worker" },
    },
    resources: {
      postgres: {
        kind: "postgres",
        provider: "digitalocean",
        version: "16",
        environments: { production: { settings: { size: "db-s-1vcpu-1gb" } } },
      },
      shared: {
        kind: "redis",
        existing_resource: "providerresource_redis",
      },
    },
  }

  const updated = updateResourceManifest(m, "postgres", "production", {
    version: "17",
    settings: { size: "db-s-2vcpu-4gb", nodes: "" },
    neededBy: ["worker"],
  })
  expect(updated.resources?.postgres).toMatchObject({
    version: "17",
    environments: { production: { settings: { size: "db-s-2vcpu-4gb" } } },
  })
  expect(updated.services?.api.needs).toBeUndefined()
  expect(updated.services?.api.need_options).toBeUndefined()
  expect(updated.services?.worker.needs).toEqual(["postgres"])

  const clearedVersion = updateResourceManifest(
    updated,
    "postgres",
    "production",
    {
      version: "",
      settings: { size: "" },
    }
  )
  expect(clearedVersion.resources?.postgres.version).toBeUndefined()
  expect(clearedVersion.resources?.postgres.environments).toBeUndefined()

  const disabledStaging = updateResourceManifest(m, "shared", "staging", {
    enabled: false,
    environments: ["staging", "production"],
  })
  expect(disabledStaging.resources?.shared.environments).toEqual({
    staging: { enabled: false },
    production: { enabled: true },
  })

  const enabledStaging = updateResourceManifest(
    disabledStaging,
    "shared",
    "staging",
    {
      enabled: true,
      environments: ["staging", "production"],
    }
  )
  expect(enabledStaging.resources?.shared.environments).toBeUndefined()
})
