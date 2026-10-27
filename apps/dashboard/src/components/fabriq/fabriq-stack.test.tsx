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
  backupPolicy: { enabled: true, everyHours: 24 },
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
    "TimescaleDB, pgvector, PostGIS. Backups daily, last restore verified yesterday."
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
    "TimescaleDB, pgvector, PostGIS. Backups daily, no restore verified yet."
  )
  expect(detail(parts, "falkordb")?.state).toBe("Running")
})

test("a project-resource Postgres is backed up with its resource, and its Redis reads as provisioned", () => {
  const parts = stackParts({
    ...docker,
    postgres: "resource",
    postgresResource: "shared-db",
    redis: "resource",
  })
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backed up with shared-db."
  )
  expect(detail(parts, "redis")?.detail).toBe(
    "Outbox stream and cache. AOF every second."
  )
})

test("a project-resource Postgres on Kubernetes names the Kubernetes limit, not a backup", () => {
  const parts = stackParts({
    ...docker,
    provider: "kubernetes",
    postgres: "resource",
    postgresResource: "shared-db",
  })
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backups on Kubernetes aren’t available yet."
  )
})

test("an engine with no choice makes no backup or persistence claim", () => {
  const parts = stackParts({ ...docker, postgres: "unset", redis: "unset" })
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS."
  )
  expect(detail(parts, "redis")?.detail).toBe("Outbox stream and cache.")
})

test("the stack reads a missing or off Postgres and Redis as not provisioned", () => {
  serve()
  const props = panelProps()
  delete props.config.composition!.dependencies.postgres
  props.config.composition!.dependencies.redis = { mode: "off" }
  mount(<FabriqStack {...props} />)
  expect(screen.getByText("TimescaleDB, pgvector, PostGIS.")).toBeVisible()
  expect(screen.getByText("Outbox stream and cache.")).toBeVisible()
  expect(screen.queryByText(/AOF every second/)).toBeNull()
})

test("an external engine is not managed by Loomplane", () => {
  const parts = stackParts({
    ...docker,
    postgres: "external",
    redis: "external",
  })
  for (const id of ["postgres", "redis"]) {
    const text = detail(parts, id)?.detail
    expect(text).toContain("Not managed by Loomplane: no backups or restores.")
    expect(text).not.toMatch(/Run by you|Backed up|Runs as|AOF/)
  }
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Not managed by Loomplane: no backups or restores."
  )
  expect(detail(parts, "redis")?.detail).toBe(
    "Outbox stream and cache. Not managed by Loomplane: no backups or restores."
  )
})

test("a provisioned engine keeps its backup and persistence copy", () => {
  const parts = stackParts(docker)
  expect(detail(parts, "postgres")?.detail).toBe(
    "TimescaleDB, pgvector, PostGIS. Backups daily, last restore verified yesterday."
  )
  expect(detail(parts, "redis")?.detail).toBe(
    "Outbox stream and cache. AOF every second."
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
      "TimescaleDB, pgvector, PostGIS. Backups daily, last restore verified yesterday."
    )
  ).toBeVisible()
  expect(
    await screen.findByText("14 applied by fabriq migrate up.")
  ).toBeVisible()
  expect(
    screen.getByText("Port 8081. 6 dynamic entity types loaded.")
  ).toBeVisible()
  const off = screen.getByText("FalkorDB").closest("section")!
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

test("the stack uses the actual policy instead of assuming daily backups", () => {
  for (const [backupPolicy, expected] of [
    [undefined, "Backups available"],
    [{ enabled: false, everyHours: 24 }, "Backups paused"],
    [{ enabled: true, everyHours: 12 }, "Backups every 12 hours"],
    [
      { enabled: true, everyHours: 24, lastError: "permission revoked" },
      "Backup schedule needs attention",
    ],
  ] as const) {
    expect(
      detail(stackParts({ ...docker, backupPolicy }), "postgres")?.detail
    ).toContain(expected)
  }
})

test.each(["unavailable", "loading"] as const)(
  "the stack does not claim daily backups when policy is %s",
  (backupPolicyStatus) => {
    expect(
      detail(stackParts({ ...docker, backupPolicyStatus }), "postgres")?.detail
    ).not.toContain("Backups daily")
  }
)

test.each([
  [
    {
      policies: [
        {
          id: "other",
          environment_id: "other",
          resource: "fabriq--postgres",
          every_hours: 24,
          enabled: true,
        },
      ],
    },
    "Backups available",
  ],
  [
    {
      policies: [
        {
          id: "other",
          environment_id: "env_prod",
          resource: "other",
          every_hours: 24,
          enabled: true,
        },
      ],
    },
    "Backups available",
  ],
  [
    {
      policies: [
        {
          id: "paused",
          environment_id: "env_prod",
          resource: "fabriq--postgres",
          every_hours: 24,
          enabled: false,
        },
      ],
    },
    "Backups paused",
  ],
  [{ policyError: 403 }, "Backup schedule access denied"],
  [{ policyError: 503 }, "Backup schedule unavailable"],
] as const)(
  "the stack scopes policy state and reports lookup failures: %s",
  async (fixture, expected) => {
    serve(fixture)
    mount(<FabriqStack {...panelProps()} />)
    expect(await screen.findByText(new RegExp(expected))).toBeVisible()
    expect(screen.queryByText(/Backups daily/)).not.toBeInTheDocument()
  }
)

test("library worker uses the approved copy with reported entity counts only", () => {
  expect(
    detail(stackParts({ ...docker, library: true }), "worker")?.detail
  ).toBe("Stock worker. 6 dynamic entity types loaded.")
  expect(
    detail(
      stackParts({ ...docker, library: true, entityTypes: undefined }),
      "worker"
    )?.detail
  ).toBe("Stock worker.")
})

test.each(["loading", "unavailable"] as const)(
  "the stack does not claim no restore verified yet when restores are %s",
  (restoreStatus) => {
    const text = detail(
      stackParts({ ...docker, restoreVerifiedAt: undefined, restoreStatus }),
      "postgres"
    )?.detail
    expect(text).not.toContain("no restore verified yet")
    expect(text).toContain("Backups daily")
  }
)

test("the stack says nothing about restores while they load", async () => {
  let release!: () => void
  serve({ hold: new Promise<void>((resolve) => (release = resolve)) })
  mount(<FabriqStack {...panelProps()} />)
  expect(
    await screen.findByText("TimescaleDB, pgvector, PostGIS. Backups daily.")
  ).toBeVisible()
  expect(screen.queryByText(/no restore verified yet/)).toBeNull()
  release()
  expect(
    await screen.findByText(/Backups daily, no restore verified yet\./)
  ).toBeVisible()
})

test.each([
  [{ backupsError: 403 }],
  [{ backupsError: 503 }],
  [{ restoresError: 403 }],
  [{ restoresError: 503 }],
] as const)(
  "the stack does not claim no restore when the restore history fails: %s",
  async (fixture) => {
    serve(fixture)
    mount(<FabriqStack {...panelProps()} />)
    expect(
      await screen.findByText(/Backups daily, restore history unavailable\./)
    ).toBeVisible()
    expect(screen.queryByText(/no restore verified yet/)).toBeNull()
  }
)
