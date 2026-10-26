import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { FabriqEngines } from "./fabriq-engines"
import { FabriqRecovery, recoveryEngines } from "./fabriq-recovery"
import { FabriqSettings } from "./fabriq-settings"
import { dependencyBinding, json, mount, panelProps, serve } from "./test-api"

const state = vi.hoisted(() => ({ allowed: true, navigate: vi.fn() }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => state.allowed }),
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => state.navigate,
  Link: ({ children }: { children: React.ReactNode }) => (
    <span>{children}</span>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  forgeClient.clear()
  state.allowed = true
  state.navigate.mockReset()
})

function withGraph() {
  const props = panelProps()
  props.config.composition!.dependencies.falkordb = {
    mode: "new",
    resource: "fabriq--falkordb",
  }
  return props
}
function api(rebuilds: unknown[] = [], failPlan = false) {
  const writes = serve()
  const base = fetch
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/rebuilds")) {
        if (init?.method === "POST") {
          const body = JSON.parse(String(init.body))
          writes.push({ method: "POST", path, body })
          const job = {
            id: "job-1",
            projection: body.projection,
            status: "queued",
            retryable: false,
            output: "",
            created_by: "u",
            created_at: new Date().toISOString(),
          }
          rebuilds.unshift(job)
          return json(job, 202)
        }
        return json({ rebuilds })
      }
      if (path.endsWith("/projects/p/plans")) {
        writes.push({
          method: "POST",
          path,
          body: JSON.parse(String(init?.body)),
        })
        return failPlan
          ? json({ error: "Planner unavailable" }, 503)
          : json({ id: "plan-1" }, 201)
      }
      return base(input, init)
    }
  )
  return writes
}

test("rebuild queues once and blocks another request while active", async () => {
  const writes = api()
  mount(<FabriqEngines {...withGraph()} />)
  const graph = screen.getByRole("region", { name: "FalkorDB" })
  const rebuild = await within(graph).findByRole("button", {
    name: "Rebuild from Postgres",
  })
  await vi.waitFor(() => expect(rebuild).toBeEnabled())
  await userEvent.dblClick(rebuild)
  expect(await within(graph).findByRole("status")).toHaveTextContent("queued")
  expect(rebuild).toBeDisabled()
  expect(writes).toHaveLength(1)
  expect(writes[0].body).toEqual({
    projection: "graph",
    request_id: expect.any(String),
  })
})

test("failed rebuild shows its output and retry only when retryable", async () => {
  api([
    {
      id: "j",
      projection: "graph",
      status: "failed",
      retryable: true,
      output: "worker unavailable",
      created_by: "u",
      created_at: "2026-10-25T10:00:00Z",
    },
  ])
  mount(<FabriqEngines {...withGraph()} />)
  expect(await screen.findByText("worker unavailable")).toBeVisible()
  expect(screen.getByRole("button", { name: "Retry rebuild" })).toBeEnabled()
})

const job = (status: string, retryable = false, output = "") => ({
  id: "j",
  projection: "graph",
  status,
  retryable,
  output,
  created_by: "u",
  created_at: "2026-10-25T10:00:00Z",
})

test.each(["queued", "running"])(
  "Rebuild from Postgres is disabled while the newest rebuild is %s",
  async (status) => {
    api([job(status)])
    mount(<FabriqEngines {...withGraph()} />)
    const graph = screen.getByRole("region", { name: "FalkorDB" })
    expect(
      await within(graph).findByRole("button", {
        name: "Rebuild from Postgres",
      })
    ).toBeDisabled()
  }
)

test("a rebuild that failed for good shows its output and can be requested again", async () => {
  const writes = api([job("failed", false, "recipe error: unknown index")])
  mount(<FabriqEngines {...withGraph()} />)
  const graph = screen.getByRole("region", { name: "FalkorDB" })
  expect(
    await within(graph).findByText("recipe error: unknown index")
  ).toBeVisible()
  expect(within(graph).queryByRole("button", { name: /Retry/ })).toBeNull()
  const rebuild = within(graph).getByRole("button", {
    name: "Rebuild from Postgres",
  })
  expect(rebuild).toBeEnabled()
  await userEvent.click(rebuild)
  await vi.waitFor(() => expect(writes).toHaveLength(1))
})

test("last rebuild names its state in words", async () => {
  api([job("succeeded")])
  mount(<FabriqEngines {...withGraph()} />)
  const graph = screen.getByRole("region", { name: "FalkorDB" })
  expect(await within(graph).findByRole("status")).toHaveTextContent(
    "completed"
  )
  expect(within(graph).queryByText("succeeded")).toBeNull()
})

test("readers cannot request projection rebuilds", async () => {
  state.allowed = false
  api()
  mount(<FabriqEngines {...withGraph()} />)
  expect(
    screen.queryByRole("button", { name: /Rebuild from Postgres/ })
  ).toBeNull()
})

test("recovery resolves provisioned and linked resources, excluding projections", () => {
  const props = withGraph()
  props.config.composition!.dependencies.postgres = {
    mode: "resource",
    resource: "shared-db",
  }
  expect(recoveryEngines(props.config)).toEqual([
    { name: "Postgres", resource: "shared-db", external: false },
    { name: "Redis", resource: "fabriq--redis", external: false },
  ])
})

test("Kubernetes recovery states its limitation without backup controls", () => {
  const props = panelProps()
  props.binding.provider = "kubernetes"
  mount(<FabriqRecovery {...props} />)
  expect(
    screen.getByText(
      "Backups for these engines aren't available on Kubernetes yet."
    )
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: /backup/i })).toBeNull()
  // No engine sections: a bare heading with nothing under it says nothing.
  expect(screen.queryByRole("region", { name: /recovery/i })).toBeNull()
  expect(screen.queryByRole("heading")).toBeNull()
})

test("Kubernetes recovery still names an external engine", () => {
  const props = panelProps()
  props.binding.provider = "kubernetes"
  props.config.composition!.dependencies.redis = {
    mode: "external",
    external: "external-cache",
  }
  mount(<FabriqRecovery {...props} />)
  expect(screen.queryByRole("region", { name: "Postgres recovery" })).toBeNull()
  expect(
    within(screen.getByRole("region", { name: "Redis recovery" })).getByText(
      "Not managed by Loomplane: no backups or restores."
    )
  ).toBeVisible()
})

test("recovery on Docker lists the Postgres backups of a live TimescaleDB binding", async () => {
  serve({
    bindings: [
      dependencyBinding("fabriq--postgres", "timescaledb"),
      dependencyBinding("fabriq--redis", "redis"),
    ],
    backups: [
      {
        id: "bk_1",
        name: "fabriq--postgres",
        environment_id: "env_prod",
        status: "succeeded",
        bytes: 2048,
        created_at: "2026-10-21T08:00:00Z",
      },
    ],
  })
  mount(<FabriqRecovery {...panelProps()} />)
  const postgres = await screen.findByRole("region", {
    name: "Postgres recovery",
  })
  expect(
    await within(postgres).findByRole("button", { name: "Back up now" })
  ).toBeEnabled()
  expect(within(postgres).getByText(/Restore evidence/)).toBeVisible()
  expect(
    within(postgres).queryByText(/source service has been removed/)
  ).toBeNull()
})

test("external engines have no managed recovery controls", () => {
  const props = panelProps()
  props.config.composition!.dependencies.postgres = {
    mode: "external",
    external: "external-pg",
  }
  props.config.composition!.dependencies.redis = {
    mode: "external",
    external: "external-cache",
  }
  mount(<FabriqRecovery {...props} />)
  expect(
    screen.getAllByText("Not managed by Loomplane: no backups or restores.")
  ).toHaveLength(2)
  expect(screen.queryByRole("button", { name: /backup/i })).toBeNull()
})

test("settings edits only changed projections then opens the review plan", async () => {
  const writes = api()
  mount(<FabriqSettings {...panelProps()} />)
  await userEvent.click(screen.getByRole("switch", { name: "FalkorDB" }))
  const review = screen.getByRole("button", { name: "Review plan" })
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  await vi.waitFor(() =>
    expect(state.navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan-1" },
    })
  )
  expect(writes.map((w) => w.body)).toEqual([
    {
      set_use: {
        resource: "fabriq",
        dependency: "falkordb",
        use: { enabled: true },
      },
    },
    { environment: "production" },
  ])
})

test("settings keeps the draft retryable when planning fails", async () => {
  api([], true)
  mount(<FabriqSettings {...panelProps()} />)
  await userEvent.click(screen.getByRole("switch", { name: "FalkorDB" }))
  const review = screen.getByRole("button", { name: "Review plan" })
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /Planner unavailable/
  )
  expect(review).toBeEnabled()
  expect(state.navigate).not.toHaveBeenCalled()
})

test("engines show deployed state and evidence without inventing a verified restore", async () => {
  api()
  const props = withGraph()
  props.components = props.components.filter(
    (b) => b.name !== "fabriq--falkordb"
  )
  mount(<FabriqEngines {...props} />)
  const graph = screen.getByRole("region", { name: "FalkorDB" })
  expect(within(graph).getByText("Not running")).toBeVisible()
  const postgres = screen.getByRole("region", { name: "Postgres" })
  expect(
    await within(postgres).findByText(/Backups daily, no restore verified yet/)
  ).toBeVisible()
  expect(within(postgres).getByText("Postgres 16")).toBeVisible()
  expect(within(postgres).getByText("Running")).toBeVisible()
  const search = screen.getByRole("region", { name: "Elasticsearch" })
  expect(
    within(search).getByText(
      "Search projections. Turn on in Settings; rebuilt from Postgres."
    )
  ).toBeVisible()
})

test("settings explains the projections before reviewing deployment", async () => {
  api()
  mount(<FabriqSettings {...panelProps()} />)
  expect(
    screen.getByText("Graph projections. Rebuilt from Postgres, so no backups.")
  ).toBeVisible()
  expect(
    screen.getByText(
      "Search projections. Rebuilt from Postgres, so no backups."
    )
  ).toBeVisible()
  // Prove the manifest has loaded, which is what enables Review plan, before
  // asserting that an untouched page leaves it disabled.
  const review = screen.getByRole("button", { name: "Review plan" })
  const graph = screen.getByRole("switch", { name: "FalkorDB" })
  await userEvent.click(graph)
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(graph)
  expect(review).toBeDisabled()
})

test("flipping a projection and back plans nothing", async () => {
  const writes = api()
  mount(<FabriqSettings {...panelProps()} />)
  const review = screen.getByRole("button", { name: "Review plan" })
  const graph = screen.getByRole("switch", { name: "FalkorDB" })
  const search = screen.getByRole("switch", { name: "Elasticsearch" })
  await userEvent.click(graph)
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(graph)
  expect(review).toBeDisabled()

  // Only the entry that really changed is planned.
  await userEvent.click(graph)
  await userEvent.click(search)
  await userEvent.click(graph)
  await vi.waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  await vi.waitFor(() => expect(state.navigate).toHaveBeenCalled())
  expect(writes.map((w) => w.body)).toEqual([
    {
      set_use: {
        resource: "fabriq",
        dependency: "elasticsearch",
        use: { enabled: true },
      },
    },
    { environment: "production" },
  ])
})

test("turning a projection off warns that it removes the engine and its data", async () => {
  api()
  const props = withGraph()
  props.config.composition!.dependencies.elasticsearch = {
    mode: "new",
    resource: "fabriq--elasticsearch",
  }
  mount(<FabriqSettings {...props} />)
  expect(screen.queryByText(/Turning .* off removes it/)).toBeNull()
  await userEvent.click(screen.getByRole("switch", { name: "FalkorDB" }))
  expect(
    screen.getByText(
      "Turning FalkorDB off removes it and its data. Graph queries fail until you turn it back on."
    )
  ).toBeVisible()
  expect(screen.queryByText(/Turning Elasticsearch off/)).toBeNull()
  await userEvent.click(screen.getByRole("switch", { name: "Elasticsearch" }))
  expect(
    screen.getByText(
      "Turning Elasticsearch off removes it and its search index. Search queries fail until you turn it back on."
    )
  ).toBeVisible()
  await userEvent.click(screen.getByRole("switch", { name: "FalkorDB" }))
  expect(screen.queryByText(/Turning FalkorDB off/)).toBeNull()
})

test("turning a projection on shows no removal warning", async () => {
  api()
  mount(<FabriqSettings {...panelProps()} />)
  await userEvent.click(screen.getByRole("switch", { name: "FalkorDB" }))
  expect(screen.queryByText(/Turning .* off removes it/)).toBeNull()
})

test("each projection switch is described by its explanation", () => {
  api()
  mount(<FabriqSettings {...panelProps()} />)
  expect(
    screen.getByRole("switch", { name: "FalkorDB" })
  ).toHaveAccessibleDescription(
    "Graph projections. Rebuilt from Postgres, so no backups."
  )
  expect(
    screen.getByRole("switch", { name: "Elasticsearch" })
  ).toHaveAccessibleDescription(
    "Search projections. Rebuilt from Postgres, so no backups."
  )
})

test("a switched-off projection's description includes the removal warning", async () => {
  api()
  mount(<FabriqSettings {...withGraph()} />)
  const graph = screen.getByRole("switch", { name: "FalkorDB" })
  await userEvent.click(graph)
  expect(graph).toHaveAccessibleDescription(
    /Rebuilt from Postgres, so no backups\..*Turning FalkorDB off removes it/
  )
})

test("engine state does not claim health for linked resources or external endpoints", async () => {
  api()
  const props = panelProps()
  props.config.composition!.dependencies.postgres = {
    mode: "external",
    external: "own-db",
  }
  props.config.composition!.dependencies.redis = {
    mode: "resource",
    resource: "shared-cache",
  }
  mount(<FabriqEngines {...props} />)
  const postgres = screen.getByRole("region", { name: "Postgres" })
  const redis = screen.getByRole("region", { name: "Redis" })
  expect(within(postgres).getByText("External")).toBeVisible()
  expect(within(postgres).queryByText("Running")).toBeNull()
  expect(within(redis).getByText("Linked")).toBeVisible()
  expect(within(redis).queryByText("Running")).toBeNull()
})

test("engines say who runs each engine: Loomplane, this project, or neither", async () => {
  api()
  const props = panelProps()
  props.config.composition!.dependencies.postgres = {
    mode: "resource",
    resource: "shared-db",
  }
  props.config.composition!.dependencies.redis = {
    mode: "resource",
    resource: "shared-cache",
  }
  const { unmount } = mount(<FabriqEngines {...props} />)
  const postgres = screen.getByRole("region", { name: "Postgres" })
  const redis = screen.getByRole("region", { name: "Redis" })
  expect(
    within(postgres).getByText(
      "TimescaleDB, pgvector and PostGIS. Backed up with shared-db."
    )
  ).toBeVisible()
  expect(
    within(redis).getByText(
      "Outbox stream and cache. Runs as shared-cache in this project."
    )
  ).toBeVisible()
  unmount()

  props.config.composition!.dependencies.postgres = {
    mode: "external",
    external: "own-db",
  }
  props.config.composition!.dependencies.redis = {
    mode: "external",
    external: "own-cache",
  }
  mount(<FabriqEngines {...props} />)
  expect(
    within(screen.getByRole("region", { name: "Postgres" })).getByText(
      "TimescaleDB, pgvector and PostGIS. Not managed by Loomplane: no backups or restores."
    )
  ).toBeVisible()
  expect(
    within(screen.getByRole("region", { name: "Redis" })).getByText(
      "Outbox stream and cache. Not managed by Loomplane: no backups or restores."
    )
  ).toBeVisible()
})
