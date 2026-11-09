// What the Nodes API answers in page tests, in its own snake_case.
import { vi } from "vitest"

export type WireNode = Record<string, unknown> & { id: string; name: string }

/** wireNode is an online node with a full inventory. */
export function wireNode(over: Record<string, unknown> = {}): WireNode {
  return {
    id: "n_one",
    org_id: "org_one",
    name: "hetzner-1",
    key_fingerprint: "sha256:3f9c1e04",
    identity_generation: 1,
    status: "online",
    endpoints: ["docker", "probe"],
    ingress_mode: "direct",
    role: "node",
    edge_scope: "",
    labels: {},
    version: "v1.9.0",
    protocol_version: 1,
    inventory: {
      os: "linux",
      kernel: "6.8",
      arch: "amd64",
      cpus: 8,
      memory_mib: 32768,
      disk_gib: 480,
      docker: { socket: "/var/run/docker.sock", version: "27.3" },
      public_addresses: ["203.0.113.24"],
    },
    last_seen_at: new Date().toISOString(),
    joined_by: "u1",
    joined_by_key_id: "",
    revoked_by: "",
    created_at: "2026-10-28T09:00:00Z",
    updated_at: "2026-10-28T09:00:00Z",
    ...over,
  } as WireNode
}

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

/**
 * serve answers the Nodes API from a mutable state and records every call.
 * Anything it doesn't know answers with an empty object.
 */
export function serve(state: {
  nodes: WireNode[]
  token?: { token: string; expires_at: string; command: string }
  keys?: unknown[]
  revokeKey?: unknown
  /** The unused join tokens, in the API's snake_case. */
  pending?: Record<string, unknown>[]
}) {
  const calls: { method: string; url: string; body?: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? "GET").toUpperCase()
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url, body })
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/nodes" && method === "GET")
        return json({ nodes: state.nodes })
      if (path === "/api/v1/nodes/join-tokens" && method === "GET")
        return json({ tokens: state.pending ?? [] })
      const cancelled = path.match(/^\/api\/v1\/nodes\/join-tokens\/([^/]+)$/)
      if (cancelled && method === "DELETE") {
        state.pending = (state.pending ?? []).filter(
          (t) => t.id !== cancelled[1]
        )
        return new Response(null, { status: 204 })
      }
      const one = path.match(/^\/api\/v1\/nodes\/([^/]+)$/)
      if (one && method === "GET") {
        const n = state.nodes.find((x) => x.id === one[1])
        return n ? json(n) : json({ error: "Not found" }, 404)
      }
      if (path === "/api/v1/nodes/join-tokens" && method === "POST")
        return json(state.token, 201)
      const gone = path.match(/^\/api\/v1\/nodes\/([^/]+)\/revoke$/)
      if (gone && method === "POST") {
        const n = state.nodes.find((x) => x.id === gone[1])
        return json({ ...n, status: "revoked" })
      }
      if (path === "/api/v1/clouds")
        return json({
          clouds: [{ id: "cloud_one", name: "Production", org_id: "org_one" }],
        })
      if (path === "/api/v1/api-keys" && method === "GET")
        return json({ keys: state.keys ?? [] })
      if (/^\/api\/v1\/api-keys\/[^/]+$/.test(path) && method === "DELETE")
        return json(state.revokeKey ?? {})
      return json({})
    }
  )
  return calls
}
