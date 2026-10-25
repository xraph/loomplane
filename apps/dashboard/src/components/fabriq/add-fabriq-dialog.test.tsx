import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AddFabriqDialog } from "@/components/fabriq/add-fabriq-dialog"

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  forgeClient.clear()
})

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

type Write = { method: string; path: string; body: unknown }

const timescale = { kind: "timescaledb", provider: "server", target: "conn_1" }

// serve stubs the API the dialog reads and writes, recording every write.
function serve(
  options: {
    resources?: Record<string, unknown>
    check?: () => Response
    edit?: () => Response
  } = {}
) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      if (method !== "GET") {
        writes.push({
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        })
        if (path.endsWith("/manifest/edits"))
          return options.edit?.() ?? json({ id: "rev_2", revision: 2 })
        if (path.endsWith("/plans"))
          return json({ id: "plan_1", status: "planning" })
        if (path.endsWith("/catalog/endpoints/checks"))
          return (
            options.check?.() ??
            json({ ok: true, version: "16.4", missing: [], problems: [] })
          )
        if (path.endsWith("/catalog/endpoints"))
          return json(
            {
              id: `ep_${writes.length}`,
              name: (init?.body && JSON.parse(String(init.body)).name) || "",
              engine: "postgres",
              host: "db.internal",
              port: 5432,
              tls: "require",
              org_id: "org",
              created_by: "u",
              created_at: "2026-10-21T12:00:00Z",
              revision: 1,
            },
            201
          )
        throw new Error(`Unexpected request ${method} ${path}`)
      }
      if (path.endsWith("/projects/p/environments"))
        return json({
          environments: [
            { id: "env_prod", name: "production" },
            { id: "env_stage", name: "staging" },
          ],
        })
      if (path.endsWith("/projects/p/manifest"))
        return json({
          id: "rev_1",
          revision: 1,
          yaml: "version: 1",
          manifest: { version: 1, resources: options.resources ?? {} },
        })
      if (path.endsWith("/connections"))
        return json({
          connections: [
            {
              id: "conn_1",
              name: "edge-1",
              cloud: "server",
              status: "ready",
              account: "edge-1",
              details: { architecture: "amd64" },
            },
            {
              id: "conn_down",
              name: "broken",
              cloud: "server",
              status: "failed",
              account: "broken",
              details: { architecture: "amd64" },
            },
          ],
        })
      throw new Error(`Unexpected request ${method} ${path}`)
    }
  )
  return writes
}

function mount() {
  const onClose = vi.fn()
  const onPlanned = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <AddFabriqDialog
        projectId="p"
        projectName="acme-shop"
        onClose={onClose}
        onPlanned={onPlanned}
      />
    </ClientProvider>
  )
  return { onClose, onPlanned }
}

const reviewPlan = () => screen.findByRole("button", { name: "Review plan" })
const engine = (name: string) => screen.getByRole("radiogroup", { name })

async function ready() {
  const button = await reviewPlan()
  await vi.waitFor(() => expect(button).toBeEnabled())
  return button
}

test("the dialog adds hosted Fabriq with the chosen dependencies", async () => {
  const writes = serve()
  const { onPlanned } = mount()
  const button = await ready()
  expect(screen.getByRole("combobox", { name: "Target" })).toHaveTextContent(
    "edge-1"
  )
  await userEvent.click(
    within(engine("FalkorDB")).getByRole("radio", { name: "Provision new" })
  )
  expect(
    screen.getByText(/Postgres 16 with TimescaleDB.*, FalkorDB, a migration/)
  ).toBeInTheDocument()
  await userEvent.click(button)

  await vi.waitFor(() => expect(onPlanned).toHaveBeenCalledWith("plan_1"))
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/manifest/edits",
      body: {
        add_resource: {
          name: "fabriq",
          kind: "fabriq",
          provider: "server",
          target: "conn_1",
          uses: {
            falkordb: { enabled: true },
            elasticsearch: { enabled: false },
          },
        },
      },
    },
    {
      method: "POST",
      path: "/api/v1/projects/p/plans",
      body: { environment: "production" },
    },
  ])
})

test("a taken name moves to the first free fabriq-N", async () => {
  const writes = serve({ resources: { fabriq: timescale } })
  mount()
  await userEvent.click(await ready())
  await vi.waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0].body).toMatchObject({ add_resource: { name: "fabriq-2" } })
})

test("connecting an existing Postgres checks, saves and selects it", async () => {
  const writes = serve()
  const { onPlanned } = mount()
  const button = await ready()
  await userEvent.click(
    within(engine("Postgres 16")).getByRole("radio", {
      name: "Connect existing",
    })
  )
  // Nothing to plan from until the connection has passed its check.
  expect(button).toBeDisabled()
  const dsn = screen.getByLabelText("Postgres connection string")
  expect(dsn).toHaveAttribute("type", "password")
  expect(dsn).toHaveAttribute("autocomplete", "new-password")
  await userEvent.type(dsn, "postgres://fabriq:s3cret@db.internal:5432/fabriq")
  expect(button).toBeDisabled()
  await userEvent.click(
    screen.getByRole("button", { name: "Check connection" })
  )
  expect(
    await screen.findByText("Connected to PostgreSQL 16.4. Ready to use.")
  ).toBeInTheDocument()
  await vi.waitFor(() => expect(button).toBeEnabled())
  expect(writes[0]).toEqual({
    method: "POST",
    path: "/api/v1/catalog/endpoints/checks",
    body: {
      engine: "postgres",
      kind: "fabriq",
      dependency: "postgres",
      target: "conn_1",
      dsn: "postgres://fabriq:s3cret@db.internal:5432/fabriq",
    },
  })

  await userEvent.click(button)
  await vi.waitFor(() => expect(onPlanned).toHaveBeenCalledWith("plan_1"))
  expect(writes.slice(1).map((w) => `${w.method} ${w.path}`)).toEqual([
    "POST /api/v1/catalog/endpoints",
    "POST /api/v1/projects/p/manifest/edits",
    "POST /api/v1/projects/p/plans",
  ])
  expect(writes[1].body).toEqual({
    name: "fabriq-postgres",
    engine: "postgres",
    dsn: "postgres://fabriq:s3cret@db.internal:5432/fabriq",
  })
  expect(writes[2].body).toMatchObject({
    add_resource: {
      name: "fabriq",
      uses: { postgres: { external: "ep_2" } },
    },
  })
  // The connection string isn't kept once it's sent.
  expect(screen.getByLabelText("Postgres connection string")).toHaveValue("")
  expect(JSON.stringify(window.location)).not.toContain("s3cret")
})

test("a failed check keeps Review plan off and says why", async () => {
  serve({
    check: () =>
      json({ ok: false, version: "16.4", missing: ["postgis"], problems: [] }),
  })
  mount()
  const button = await ready()
  await userEvent.click(
    within(engine("Postgres 16")).getByRole("radio", {
      name: "Connect existing",
    })
  )
  await userEvent.type(
    screen.getByLabelText("Postgres connection string"),
    "postgres://u@db.internal/fabriq"
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Check connection" })
  )
  const note = await screen.findByRole("note")
  expect(note).toHaveTextContent(
    "Connected to PostgreSQL 16.4. Missing extension: postgis. Ask your database admin to install it, or provision a new one."
  )
  expect(button).toBeDisabled()
  // Changing the string throws the result away.
  await userEvent.type(screen.getByLabelText("Postgres connection string"), "x")
  expect(screen.queryByRole("note")).not.toBeInTheDocument()
})

test("a resource already in the project is offered above the connection string", async () => {
  const writes = serve({
    resources: {
      db: timescale,
      cache: { kind: "valkey", provider: "server", target: "conn_1" },
      elsewhere: { kind: "redis", provider: "server", target: "conn_2" },
      plain: { kind: "postgres", provider: "server", target: "conn_1" },
    },
  })
  const { onPlanned } = mount()
  const button = await ready()
  await userEvent.click(
    within(engine("Postgres 16")).getByRole("radio", {
      name: "Connect existing",
    })
  )
  await userEvent.click(
    within(engine("Redis")).getByRole("radio", { name: "Connect existing" })
  )
  expect(
    screen.getByRole("radio", { name: "db (in this project)" })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("radio", { name: "cache (in this project)" })
  ).toBeInTheDocument()
  // Another target's resource, and a Postgres without TimescaleDB, aren't.
  expect(screen.queryByText(/elsewhere/)).not.toBeInTheDocument()
  expect(screen.queryByText(/plain/)).not.toBeInTheDocument()
  expect(button).toBeDisabled()

  await userEvent.click(
    screen.getByRole("radio", { name: "db (in this project)" })
  )
  await userEvent.click(
    screen.getByRole("radio", { name: "cache (in this project)" })
  )
  // A resource needs no connection string or check.
  expect(
    screen.queryByLabelText("Postgres connection string")
  ).not.toBeInTheDocument()
  await vi.waitFor(() => expect(button).toBeEnabled())
  await userEvent.click(button)
  await vi.waitFor(() => expect(onPlanned).toHaveBeenCalled())
  expect(writes[0].body).toMatchObject({
    add_resource: {
      uses: { postgres: { resource: "db" }, redis: { resource: "cache" } },
    },
  })
  expect(writes.some((w) => w.path.endsWith("/catalog/endpoints"))).toBe(false)
})

test("library mode adds fabriq-library", async () => {
  const writes = serve()
  const { onPlanned } = mount()
  const button = await ready()
  await userEvent.click(screen.getByRole("radio", { name: /Library only/ }))
  expect(
    screen.getByText(/a migration job and the Fabriq worker\.$/)
  ).toBeInTheDocument()
  await userEvent.click(button)
  await vi.waitFor(() => expect(onPlanned).toHaveBeenCalledWith("plan_1"))
  expect(writes[0].body).toMatchObject({
    add_resource: { kind: "fabriq-library" },
  })
})

test("a refused manifest edit says so and plans nothing", async () => {
  const writes = serve({
    edit: () => json({ message: "The manifest already has fabriq." }, 409),
  })
  const { onPlanned } = mount()
  await userEvent.click(await ready())
  expect(
    await screen.findByText(/The manifest already has fabriq/)
  ).toBeInTheDocument()
  expect(onPlanned).not.toHaveBeenCalled()
  expect(writes).toHaveLength(1)
})

test("Cancel closes without writing", async () => {
  const writes = serve()
  const { onClose } = mount()
  await ready()
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(onClose).toHaveBeenCalled()
  expect(writes).toEqual([])
})

test("dialog copy includes the documented backup availability departure", async () => {
  serve()
  mount()
  await ready()
  const dialog = screen.getByRole("dialog")
  for (const text of [
    "Add Fabriq to acme-shop",
    "One click sets up the whole stack. Swap any engine for one you already run.",
    "Environment",
    "Target",
    "Engines",
    "This creates",
    "Hosted server",
    "Apps in any language connect with a fabriq:// key. Includes the admin console.",
    "Library only",
    "Your Go app embeds Fabriq. Loomplane runs the engines, migrations and worker.",
    "Postgres 16",
    "With TimescaleDB, pgvector and PostGIS. Backups on Docker; configure a schedule in Recovery.",
    "Redis",
    "Carries the outbox stream and live subscriptions.",
    "FalkorDB",
    "Graph projections. Rebuilt from Postgres, so no backups.",
    "Elasticsearch",
    "Search projections. Rebuilt from Postgres, so no backups.",
    "Nothing is created until you approve the plan.",
    "Cancel",
    "Review plan",
  ])
    expect(within(dialog).getByText(text)).toBeInTheDocument()
  expect(within(dialog).getAllByText("required")).toHaveLength(2)
  expect(within(dialog).getAllByText("optional")).toHaveLength(2)
  expect(within(dialog).getAllByText("Provision new")).toHaveLength(4)
  expect(within(dialog).getAllByText("Connect existing")).toHaveLength(2)
  expect(within(dialog).getAllByText("Off")).toHaveLength(2)

  await userEvent.click(
    within(engine("Postgres 16")).getByRole("radio", {
      name: "Connect existing",
    })
  )
  await userEvent.click(
    within(engine("Redis")).getByRole("radio", { name: "Connect existing" })
  )
  expect(
    within(dialog).getByPlaceholderText(
      "postgres://user@db.internal:5432/fabriq"
    )
  ).toBeInTheDocument()
  expect(
    within(dialog).getByPlaceholderText("redis://cache.internal:6379")
  ).toBeInTheDocument()
  expect(within(dialog).getByLabelText("Redis address")).toHaveAttribute(
    "type",
    "password"
  )
  expect(
    within(dialog).getAllByRole("button", { name: "Check connection" })
  ).toHaveLength(2)
  expect(
    within(dialog).getAllByText(
      "Not managed by Loomplane: no backups or restores. Fabriq only runs its own migrations on it."
    )
  ).toHaveLength(2)
})

test("radio choices support arrow keys without submitting the dialog", async () => {
  const writes = serve({ resources: { shared: timescale } })
  mount()
  await ready()
  const mode = screen.getByRole("radio", { name: /^Hosted server/ })
  mode.focus()
  await userEvent.keyboard("{ArrowRight}")
  expect(screen.getByRole("radio", { name: /^Library only/ })).toBeChecked()
  const postgres = within(engine("Postgres 16")).getByRole("radio", {
    name: "Provision new",
  })
  postgres.focus()
  await userEvent.keyboard("{ArrowRight}")
  expect(
    within(engine("Postgres 16")).getByRole("radio", {
      name: "Connect existing",
    })
  ).toBeChecked()
  const connection = screen.getByRole("radio", { name: "A connection string" })
  connection.focus()
  await userEvent.keyboard("{ArrowLeft}")
  expect(
    screen.getByRole("radio", { name: "shared (in this project)" })
  ).toBeChecked()
  expect(await reviewPlan()).toBeEnabled()
  expect(writes).toEqual([])
})
