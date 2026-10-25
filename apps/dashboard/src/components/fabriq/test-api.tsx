// A stub of the API the Fabriq panel reads and writes, shared by its tests.
import { ClientProvider } from "@forge-go/client-react"
import { render } from "@testing-library/react"
import type { ReactElement } from "react"
import { vi } from "vitest"

import type { Binding } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import type { CatalogPanelProps, ResourceConfigView } from "@/lib/catalog-kinds"

export function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

export type Write = { method: string; path: string; body: unknown }

type Key = {
  service: string
  tenant: string
  prefix?: string
  state: string
  created_at?: string
  last_used_at?: string
  revoked_at?: string
  last_used_known: boolean
}

type Tenant = { id: string; label: string; created_at: string }

export type Fixture = {
  /** Fabriq answering 503 on tenants and keys, with this reason. */
  down?: string
  status?: Record<string, unknown>
  tenants?: Tenant[]
  keys?: Key[]
  services?: Record<string, unknown>
  policies?: readonly unknown[]
  policyError?: number
  backups?: unknown[]
  restores?: unknown[]
  adminUrl?: string
}

const minutesAgo = (n: number) =>
  new Date(Date.now() - n * 60_000).toISOString()

export const acme: Tenant = {
  id: "acme",
  label: "acme",
  created_at: "2026-10-02T09:00:00Z",
}
export const globex: Tenant = {
  id: "globex",
  label: "globex",
  created_at: "2026-10-09T09:00:00Z",
}

export const apiKey: Key = {
  service: "api",
  tenant: "acme",
  prefix: "fq_7c2e",
  state: "active",
  created_at: "2026-10-02T09:00:00Z",
  last_used_at: minutesAgo(2),
  last_used_known: true,
}

/** The needs of services in the manifest: api and billing use acme. */
export const manifestServices = {
  api: { needs: ["fabriq"], need_options: { fabriq: { tenant: "acme" } } },
  billing: { needs: ["fabriq"], need_options: { fabriq: { tenant: "acme" } } },
  jobs: { needs: [] },
  web: {
    needs: ["fabriq", "postgres"],
    need_options: { fabriq: { tenant: "globex" } },
  },
}

/**
 * serve stubs the API. It returns what was written, and keeps the tenants
 * and keys the writes change so a refetch sees them.
 */
export function serve(fixture: Fixture = {}) {
  const writes: Write[] = []
  const tenants = [...(fixture.tenants ?? [acme, globex])]
  const keys = [...(fixture.keys ?? [apiKey])]
  const down = () =>
    json({ error: `Fabriq isn't reachable right now: ${fixture.down}` }, 503)
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      const fabriq = "/projects/p/environments/env_prod/fabriq/fabriq"
      if (method !== "GET") writes.push({ method, path, body })
      if (path.endsWith("/catalog/fabriq/admin-sessions"))
        return json(
          {
            url: fixture.adminUrl ?? "/catalog-admin/session-1/",
            expires_at: "2026-10-22T20:00:00Z",
          },
          201
        )
      if (path.endsWith("/manifest/edits"))
        return json({ id: "rev_2", revision: 2 })
      if (path.endsWith(`${fabriq}/tenants`)) {
        if (fixture.down) return down()
        if (method === "POST") {
          const tenant = {
            id: body.tenantId ?? body.tenant_id,
            label: body.tenantId ?? body.tenant_id,
            created_at: new Date().toISOString(),
          }
          tenants.push(tenant)
          return json(tenant, 201)
        }
        return json({ tenants })
      }
      if (/\/keys\/[^/]+\/(rotate|revoke)$/.test(path)) {
        const [, service, action] = path.match(/\/keys\/([^/]+)\/(\w+)$/)!
        const key = keys.find((k) => k.service === service)!
        if (action === "rotate") key.state = "rotating"
        else {
          key.state = "revoked"
          key.revoked_at = new Date().toISOString()
        }
        return json(key)
      }
      if (path.endsWith(`${fabriq}/keys`))
        return fixture.down ? down() : json({ keys })
      if (path.endsWith(fabriq))
        return json(
          fixture.status ?? {
            kind: "fabriq",
            version: "1.9",
            reachable: !fixture.down,
            ...(fixture.down ? { reason: fixture.down } : {}),
            migrations: 14,
            migrations_known: true,
            entity_types: 6,
            keys_report_use: true,
          }
        )
      if (path.endsWith("/projects/p/environments"))
        return json({ environments: [{ id: "env_prod", name: "production" }] })
      if (path.endsWith("/projects/p/manifest"))
        return json({
          id: "rev_1",
          revision: 1,
          yaml: "version: 1",
          manifest: {
            version: 1,
            services: fixture.services ?? manifestServices,
          },
        })
      if (path.endsWith("/projects/p/backup-policies"))
        return fixture.policyError
          ? json(
              { message: "Cannot read backup policies" },
              fixture.policyError
            )
          : json({
              policies: fixture.policies ?? [
                {
                  id: "policy",
                  environment_id: "env_prod",
                  resource: "fabriq--postgres",
                  enabled: true,
                  every_hours: 24,
                },
              ],
            })
      if (path.endsWith("/projects/p/service-backups"))
        return json({ backups: fixture.backups ?? [] })
      if (path.endsWith("/projects/p/service-restores"))
        return json({ restores: fixture.restores ?? [] })
      if (path.endsWith("/connections"))
        return json({
          connections: [
            { id: "conn_1", name: "edge-1", cloud: "server", status: "ready" },
          ],
        })
      throw new Error(`Unexpected request ${method} ${path}`)
    }
  )
  return writes
}

const binding = (name: string, extra: Partial<Binding> = {}): Binding => ({
  id: `b_${name}`,
  name,
  subject: "resource",
  provider: "server",
  engine: "fabriq",
  environmentId: "env_prod",
  connectionId: "conn_1",
  updatedAt: minutesAgo(12),
  config: "{}",
  commitSha: "",
  createdAt: minutesAgo(30),
  drift: "",
  driftMessage: "",
  externalId: "",
  orgId: "org",
  planId: "plan",
  projectId: "p",
  region: "",
  revision: "1",
  url: "",
  ...extra,
})

/** The props a catalog panel is given for a Fabriq on Docker. */
export function panelProps(
  overrides: Partial<CatalogPanelProps> = {}
): CatalogPanelProps {
  const config: ResourceConfigView = {
    kind: "fabriq",
    provider: "server",
    composition: {
      kind: "fabriq",
      version: "1.9",
      dependencies: {
        postgres: { mode: "new", resource: "fabriq--postgres" },
        redis: { mode: "new", resource: "fabriq--redis" },
        falkordb: { mode: "off" },
        elasticsearch: { mode: "off" },
      },
    },
  }
  const own = binding("fabriq", { config: JSON.stringify(config) })
  return {
    projectId: "p",
    environmentId: "env_prod",
    resource: "fabriq",
    kind: "fabriq",
    binding: own,
    config,
    components: [
      "fabriq--postgres",
      "fabriq--redis",
      "fabriq--migrate",
      "fabriq--server",
      "fabriq--admin",
    ].map((name) => binding(name)),
    ...overrides,
  }
}

export function mount(ui: ReactElement) {
  return render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)
}
