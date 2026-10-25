import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { deployDatabase } from "@/lib/catalog"
import type { ManagedDatabaseReview } from "@/lib/managed-catalog"
import {
  managedDatabaseFixture,
  managedWire,
} from "@/test-fixtures/managed-database"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  vi.useRealTimers()
})

function managedServer(
  target: "rds" | "cloud-sql",
  deploymentStatus = () => "succeeded"
) {
  const fixture = managedDatabaseFixture(target)
  const calls: {
    method: string
    path: string
    body: Record<string, unknown>
  }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<
        string,
        unknown
      >
      calls.push({ method, path, body })
      let response: unknown
      if (method === "POST" && path.endsWith("/projects"))
        response = { id: "project", status: "ready" }
      else if (path.endsWith("/projects/project"))
        response = { id: "project", status: "ready" }
      else if (
        method === "PUT" &&
        path.endsWith(`/connections/${fixture.provider.cloud}`)
      )
        response = {
          environment: "production",
          connections: { [fixture.provider.cloud]: "selected" },
        }
      else if (path.endsWith("/manifest")) response = { id: "manifest" }
      else if (path.endsWith("/approve")) response = { id: "deployment" }
      else if (path.endsWith("/deployment"))
        response = { id: "deployment", status: deploymentStatus() }
      else if (path.endsWith("/plans") || path.endsWith("/plans/plan"))
        response = fixture.plan
      else throw new Error(`Unreviewed request ${method} ${path}`)
      return new Response(JSON.stringify(managedWire(response)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return { fixture, calls }
}

const pscEndpoint = {
  consumerProject: "project-a",
  network: "projects/project-a/global/networks/apps",
  forwardingRule:
    "projects/project-a/regions/us-central1/forwardingRules/database",
}

it("saves and approves only the requested PSC source endpoint", async () => {
  const { fixture, calls } = managedServer("cloud-sql")
  const managed: ManagedDatabaseReview = {
    ...fixture.review,
    profile: {
      ...fixture.profile,
      id: "postgresql-17-cloud-sql-psc",
      access: "cloud-sql-psc-connector",
    },
    cloudSqlPsc: { ...pscEndpoint },
  }
  fixture.plan.actions[0].resource!.managed!.profile = managed.profile
  fixture.plan.actions[0].resource!.managed!.cloudSqlPsc = { ...pscEndpoint }
  await deployDatabase(
    {
      name: "Private orders",
      kind: "postgres",
      provider: "cloud-sql",
      target: "selected",
      daily: false,
      managed,
    },
    vi.fn(),
    vi.fn()
  )
  const yaml = String(
    calls.find((call) => call.path.endsWith("/manifest"))?.body.yaml
  )
  expect(yaml).toContain("    cloud_sql_psc:\n")
  expect(yaml).toContain(
    `      consumer_project: "${pscEndpoint.consumerProject}"`
  )
  expect(yaml).toContain(`      network: "${pscEndpoint.network}"`)
  expect(yaml).toContain(
    `      forwarding_rule: "${pscEndpoint.forwardingRule}"`
  )
  expect(calls.filter((call) => call.path.endsWith("/approve"))).toHaveLength(1)
})

it("rejects missing, cross-region and malformed PSC routes before creating a project", async () => {
  const fetcher = vi.fn(async () => {
    throw new Error("unexpected request")
  })
  vi.stubGlobal("fetch", fetcher)
  const { review } = managedDatabaseFixture("cloud-sql")
  for (const cloudSqlPsc of [
    undefined,
    { ...pscEndpoint, network: `${pscEndpoint.network}/extra` },
    { ...pscEndpoint, consumerProject: "other-project" },
    {
      ...pscEndpoint,
      forwardingRule: pscEndpoint.forwardingRule.replace(
        "us-central1",
        "europe-west1"
      ),
    },
    {
      ...pscEndpoint,
      forwardingRule: `https://www.googleapis.com/compute/v1/${pscEndpoint.forwardingRule}`,
    },
  ]) {
    await expect(
      deployDatabase(
        {
          name: "Orders",
          kind: "postgres",
          provider: "cloud-sql",
          target: "selected",
          daily: false,
          managed: {
            ...review,
            profile: {
              ...review.profile,
              id: "postgresql-17-cloud-sql-psc",
              access: "cloud-sql-psc-connector",
            },
            cloudSqlPsc,
          },
        },
        vi.fn(),
        vi.fn()
      )
    ).rejects.toThrow(/PSC/)
  }
  expect(fetcher).not.toHaveBeenCalled()
})

for (const target of ["rds", "cloud-sql"] as const) {
  it(`deploys ${target} through its selected account without container manifests or backup schedules`, async () => {
    const { fixture, calls } = managedServer(target)
    const created = vi.fn()
    const id = await deployDatabase(
      {
        name: "Orders",
        kind: "postgres",
        provider: target,
        target: "selected",
        daily: false,
        managed: fixture.review,
      },
      vi.fn(),
      created
    )
    expect(id).toBe("project")
    expect(created).toHaveBeenCalledExactlyOnceWith("project")
    const choice = calls.findIndex(
      (c) =>
        c.method === "PUT" &&
        c.path.endsWith(`/connections/${fixture.provider.cloud}`)
    )
    const manifest = calls.findIndex((c) => c.path.endsWith("/manifest"))
    expect(choice).toBeGreaterThan(0)
    expect(choice).toBeLessThan(manifest)
    expect(calls[choice].body).toEqual({ connection_id: "selected" })
    const yaml = String(calls[manifest].body.yaml)
    expect(yaml).toContain(`provider: ${target}`)
    expect(yaml).toContain(`region: "${fixture.review.region}"`)
    expect(yaml).not.toMatch(/target:|image:|volume/)
    expect(calls.filter((c) => c.path.endsWith("/approve"))).toHaveLength(1)
    expect(calls.some((c) => c.path.includes("backup-policies"))).toBe(false)
  })
}

it("rejects unsupported managed backup schedules before creating a project", async () => {
  const fetcher = vi.fn()
  vi.stubGlobal("fetch", fetcher)
  const { review } = managedDatabaseFixture("rds")
  await expect(
    deployDatabase(
      {
        name: "Orders",
        kind: "postgres",
        provider: "rds",
        target: "selected",
        daily: true,
        managed: review,
      },
      vi.fn(),
      vi.fn()
    )
  ).rejects.toThrow(/managed.*backup|backup.*managed/i)
  expect(fetcher).not.toHaveBeenCalled()
})

it("keeps the created project available after account selection is refused", async () => {
  const { review } = managedDatabaseFixture("rds")
  const writes: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (init?.method === "POST") writes.push(path)
      const denied = init?.method === "PUT" && path.endsWith("/connections/aws")
      return new Response(
        JSON.stringify(
          denied
            ? { error: "Account selection denied" }
            : { id: "project", status: "ready" }
        ),
        {
          status: denied ? 403 : 200,
          headers: { "Content-Type": "application/json" },
        }
      )
    }
  )
  const created = vi.fn()
  await expect(
    deployDatabase(
      {
        name: "Orders",
        kind: "postgres",
        provider: "rds",
        target: "selected",
        daily: false,
        managed: review,
      },
      vi.fn(),
      created
    )
  ).rejects.toThrow("Account selection denied")
  expect(created).toHaveBeenCalledExactlyOnceWith("project")
  expect(writes).toHaveLength(1)
})

it("keeps following managed provisioning beyond the container wait window", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] })
  const started = Date.now()
  const { fixture } = managedServer("rds", () =>
    Date.now() - started >= 300_000 ? "succeeded" : "running"
  )
  const outcome = deployDatabase(
    {
      name: "Orders",
      kind: "postgres",
      provider: "rds",
      target: "selected",
      daily: false,
      managed: fixture.review,
    },
    vi.fn(),
    vi.fn()
  ).catch((error: unknown) => error)
  await vi.advanceTimersByTimeAsync(305_000)
  expect(await outcome).toBe("project")
})
