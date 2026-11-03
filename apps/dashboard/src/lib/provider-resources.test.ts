import { afterEach, expect, test, vi } from "vitest"
import type { Connection, Provider } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import {
  filterProviderResources,
  managedOffers,
  providerResourceReadiness,
  resourceOfferings,
  reviewProviderResource,
  type InventoryResource,
} from "@/lib/provider-resources"

const resource: InventoryResource = {
  id: "r",
  provider: "digitalocean",
  connectionId: "c",
  accountIdentity: "account",
  externalId: "db",
  name: "orders",
  kind: "postgres",
  region: "nyc3",
  status: "online",
  ownership: "external",
  endpointId: "",
  observedAt: "2026-10-31T12:00:00Z",
  revision: 1,
}
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

test("filters inventory by ownership, connection, engine and searchable provider facts", () => {
  const other = {
    ...resource,
    id: "other",
    connectionId: "b",
    ownership: "loomplane",
    kind: "mysql",
  }
  expect(
    filterProviderResources([resource, other], {
      search: "NYC3",
      ownership: "external",
      connection: "c",
      kind: "postgres",
    })
  ).toEqual([resource])
  expect(
    filterProviderResources([resource, other], { search: "missing" })
  ).toEqual([])
})
test("keeps discovery freshness separate from stored credentials and target verification", () => {
  const now = new Date("2026-10-31T12:15:00Z").getTime()
  expect(providerResourceReadiness(resource, now)).toEqual({
    discovery: "Observed",
    credentials: "Credentials needed",
    network: "Verify in deployment",
  })
  expect(
    providerResourceReadiness({ ...resource, endpointId: "ep" }, now)
      .credentials
  ).toBe("Credentials saved")
  expect(providerResourceReadiness(resource, now + 31 * 60_000).discovery).toBe(
    "Stale"
  )
  expect(
    providerResourceReadiness({ ...resource, status: "missing" }, now).discovery
  ).toBe("Missing")
})
test("offers registry provisions only on available providers with ready matching accounts", () => {
  const providers = [
    {
      name: "new-provider",
      cloud: "new-cloud",
      available: true,
      provisions: [
        { kind: "new-kind", creates: "Managed thing" },
        {
          kind: "native-import",
          creates: "Imported namespace",
          importOnly: true,
        },
      ],
    },
    {
      name: "paused",
      cloud: "new-cloud",
      available: false,
      provisions: [{ kind: "postgres" }],
    },
  ] as Provider[]
  const connections = [
    { id: "ready", cloud: "new-cloud", status: "ready" },
    { id: "failed", cloud: "new-cloud", status: "failed" },
  ] as Connection[]
  expect(
    resourceOfferings(providers, connections).map((o) => [
      o.provider.name,
      o.offer.kind,
      o.connection.id,
    ])
  ).toEqual([["new-provider", "new-kind", "ready"]])
})
test("collapses each provider's offering into one catalog card across its accounts", () => {
  const providers = [
    {
      name: "cloudflare",
      title: "Cloudflare",
      cloud: "cloudflare",
      available: true,
      provisions: [
        { kind: "cloudflare-d1", creates: "Cloudflare D1 database" },
        { kind: "cloudflare-durable-object", creates: "DO", importOnly: true },
      ],
    },
  ] as Provider[]
  const connections = [
    { id: "a", cloud: "cloudflare", status: "ready" },
    { id: "b", cloud: "cloudflare", status: "ready" },
  ] as Connection[]
  expect(managedOffers(providers, connections)).toEqual([
    {
      kind: "cloudflare-d1",
      provider: "cloudflare",
      cloud: "cloudflare",
      providerTitle: "Cloudflare",
      creates: "Cloudflare D1 database",
      accounts: 2,
    },
  ])
})
test("adds an external reference then creates a review without applying infrastructure", async () => {
  const calls: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      calls.push({ path, body: JSON.parse(String(init?.body)) })
      return Response.json(
        path.endsWith("edits") ? { revision: 2 } : { id: "review" }
      )
    }
  )
  await expect(
    reviewProviderResource(
      {
        projectId: "p",
        environment: "production",
        name: "orders",
        kind: "postgres",
        existingResource: "r",
        service: "api",
      },
      () => {}
    )
  ).resolves.toEqual({ planId: "review" })
  expect(calls).toEqual([
    {
      path: "/api/v1/projects/p/manifest/edits",
      body: {
        add_resource: {
          name: "orders",
          kind: "postgres",
          existing_resource: "r",
          enabled_in: "production",
        },
      },
    },
    {
      path: "/api/v1/projects/p/manifest/edits",
      body: { link: { service: "api", need: "orders" } },
    },
    { path: "/api/v1/projects/p/plans", body: { environment: "production" } },
  ])
})
test("a denied manifest edit stops before planning", async () => {
  const calls: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    calls.push(String(input))
    return Response.json({ message: "Project access denied" }, { status: 403 })
  })
  await expect(
    reviewProviderResource(
      {
        projectId: "p",
        environment: "production",
        name: "orders",
        kind: "postgres",
        existingResource: "r",
      },
      () => {}
    )
  ).rejects.toThrow()
  expect(calls).toHaveLength(1)
})
test("an incomplete sync marks recent observations stale until a complete account refresh", () => {
  expect(
    providerResourceReadiness(
      { ...resource, observationState: "stale" },
      new Date("2026-10-31T12:15:00Z").getTime()
    ).discovery
  ).toBe("Stale")
})
test("account filtering follows verified account identity across duplicate connections", () => {
  const rows = [
    resource,
    { ...resource, id: "other-provider", provider: "azure" },
  ]
  expect(
    filterProviderResources(rows, {
      connection: "duplicate",
      accountIdentity: "account",
      provider: "digitalocean",
    })
  ).toEqual([resource])
})
test("native creation pins the checked account and chosen region before review", async () => {
  const calls: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      calls.push({ path, body: JSON.parse(String(init?.body)) })
      return Response.json(
        path.endsWith("edits")
          ? { manifest: { version: 1, resources: { cache: {} } } }
          : { id: "review" }
      )
    }
  )
  await reviewProviderResource(
    {
      projectId: "p",
      environment: "staging",
      name: "cache",
      kind: "redis",
      provider: "azure-managed-redis",
      target: "azure-ready",
      region: "westus2",
    },
    () => {}
  )
  expect(calls).toEqual([
    {
      path: "/api/v1/projects/p/manifest/edits",
      body: {
        add_resource: {
          name: "cache",
          kind: "redis",
          provider: "azure-managed-redis",
          target: "azure-ready",
          region: "westus2",
          enabled_in: "staging",
        },
      },
    },
    {
      path: "/api/v1/projects/p/plans",
      body: { environment: "staging", catalog: true },
    },
  ])
})
test("a failed consumer link reports the persisted edit and stops before planning", async () => {
  let writes = 0
  vi.stubGlobal("fetch", async () => {
    writes++
    return writes === 1
      ? Response.json({ revision: 2 })
      : Response.json({ message: "Service access denied" }, { status: 403 })
  })
  await expect(
    reviewProviderResource(
      {
        projectId: "p",
        environment: "production",
        name: "orders",
        kind: "postgres",
        existingResource: "r",
        service: "api",
      },
      () => {}
    )
  ).rejects.toThrow(
    "orders is in your manifest, but review couldn't start: Service access denied"
  )
  expect(writes).toBe(2)
})
test("scoped account connections match the server's connection identity", () => {
  const scoped = {
    ...resource,
    accountIdentity: "canonical-account",
    connectionIdentity: "region-scoped-account",
  }
  expect(
    filterProviderResources([scoped], {
      connection: "duplicate",
      accountIdentity: "region-scoped-account",
      provider: "digitalocean",
    })
  ).toEqual([scoped])
})
