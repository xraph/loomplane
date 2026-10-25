import { screen, within } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { FabriqStack } from "@/components/fabriq/fabriq-stack"
import { mount, panelProps, serve } from "@/components/fabriq/test-api"
import { stackParts, type StackInput } from "@/lib/fabriq"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const now = Date.parse("2026-10-22T12:00:00Z")

const docker: StackInput = {
  provider: "server",
  postgres: "managed",
  redis: "managed",
  restoreVerifiedAt: "2026-10-21T08:00:00Z",
  now,
  falkordb: false,
  elasticsearch: false,
  migrations: 14,
  entityTypes: 6,
}

const detail = (parts: ReturnType<typeof stackParts>, id: string) =>
  parts.find((p) => p.id === id)

test("stack cards on Docker and Kubernetes", () => {
  const parts = stackParts(docker)
  expect(parts.map((p) => [p.name, p.state])).toEqual([
    ["Postgres 16", "Running"],
    ["Redis", "Running"],
    ["Migrations", "Completed"],
    ["Fabriq server", "Running"],
    ["Admin console", "Running"],
    ["FalkorDB", "Off"],
    ["Elasticsearch", "Off"],
  ])
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backups available, last restore verified yesterday."
  )
  expect(detail(parts, "redis")?.detail).toBe(
    "Outbox stream and cache. AOF every second."
  )
  expect(detail(parts, "migrations")?.detail).toBe(
    "14 applied by fabriq migrate up."
  )
  expect(detail(parts, "server")?.detail).toBe(
    "Port 8081. 6 dynamic entity types loaded."
  )
  expect(detail(parts, "admin")?.detail).toBe(
    "Opens through Loomplane, already signed in."
  )
  expect(detail(parts, "falkordb")?.detail).toBe(
    "Graph projections. Turn on in Settings; rebuilt from Postgres."
  )
  expect(detail(parts, "elasticsearch")?.detail).toBe(
    "Search projections. Turn on in Settings; rebuilt from Postgres."
  )

  const cluster = stackParts({ ...docker, provider: "kubernetes" })
  expect(detail(cluster, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backups on Kubernetes aren’t available yet."
  )
})

test("the stack says what Fabriq doesn't report", () => {
  const parts = stackParts({
    ...docker,
    migrations: undefined,
    entityTypes: undefined,
    restoreVerifiedAt: undefined,
    down: ["server"],
    falkordb: true,
  })
  expect(detail(parts, "migrations")?.detail).toBe(
    "Applied by fabriq migrate up."
  )
  expect(detail(parts, "server")).toMatchObject({
    state: "Not running",
    detail: "Port 8081.",
  })
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backups available, no restore verified yet."
  )
  expect(detail(parts, "falkordb")?.state).toBe("Running")
})

test("a Postgres Loomplane doesn't run has no backups to describe", () => {
  const parts = stackParts({ ...docker, postgres: "linked", redis: "linked" })
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Connected to your engine. See Recovery for its backup options."
  )
  expect(detail(parts, "redis")?.detail).toBe(
    "Outbox stream and cache. Run by you."
  )
})

test("the stack reads the bindings, the status and the last verified restore", async () => {
  const day = 24 * 60 * 60_000
  serve({
    backups: [
      {
        id: "bk_1",
        name: "fabriq--postgres",
        environment_id: "env_prod",
        status: "succeeded",
      },
      {
        id: "bk_other",
        name: "elsewhere",
        environment_id: "env_prod",
        status: "succeeded",
      },
    ],
    restores: [
      {
        id: "r_1",
        backup_id: "bk_1",
        status: "succeeded",
        verified_at: new Date(Date.now() - day).toISOString(),
      },
      {
        id: "r_other",
        backup_id: "bk_other",
        status: "succeeded",
        verified_at: new Date().toISOString(),
      },
    ],
  })
  mount(<FabriqStack {...panelProps()} />)
  expect(
    await screen.findByText(
      "Set up in one click · migrations applied 12 minutes ago"
    )
  ).toBeVisible()
  expect(
    await screen.findByText(
      "TimescaleDB, pgvector, PostGIS. Backups available, last restore verified yesterday."
    )
  ).toBeVisible()
  expect(
    await screen.findByText("14 applied by fabriq migrate up.")
  ).toBeVisible()
  expect(
    screen.getByText("Port 8081. 6 dynamic entity types loaded.")
  ).toBeVisible()
  const off = screen.getByText("FalkorDB").closest("div")!
  expect(within(off).getByText("Off")).toBeVisible()
  expect(screen.getAllByText("Running")).toHaveLength(4)
  expect(screen.getByText("Completed")).toBeVisible()
})

test("a Fabriq on Kubernetes says backups aren't there yet", async () => {
  serve()
  const props = panelProps()
  mount(
    <FabriqStack
      {...props}
      binding={{ ...props.binding, provider: "kubernetes" }}
    />
  )
  expect(
    await screen.findByText(
      "TimescaleDB, pgvector, PostGIS. Backups on Kubernetes aren’t available yet."
    )
  ).toBeVisible()
})

test("the stack still shows when Fabriq isn't reachable", async () => {
  serve({ down: "connection refused" })
  mount(<FabriqStack {...panelProps()} />)
  expect(await screen.findByText("Applied by fabriq migrate up.")).toBeVisible()
  expect(screen.getByText("Port 8081.")).toBeVisible()
  expect(screen.getByText("Postgres 16")).toBeVisible()
})
