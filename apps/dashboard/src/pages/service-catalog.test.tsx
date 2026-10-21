import { ClientProvider } from "@forge-go/client-react"
import { act, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ServiceCatalog } from "@/pages/service-catalog"
import * as catalog from "@/lib/catalog"
import { useProvidersList } from "@/client/forge/loomplane/hooks"
import {
  managedDatabaseFixture,
  managedWire,
} from "@/test-fixtures/managed-database"

const permissions = vi.hoisted(() => ({ allowed: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permissions.allowed }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    ...props
  }: {
    children: React.ReactNode
    to: string
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.allowed = true
  vi.restoreAllMocks()
})
function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <ServiceCatalog />
    </ClientProvider>
  )
}
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

for (const target of ["rds", "cloud-sql"] as const) {
  it(`shows ${target} account, requested storage and provider backups without a Docker schedule`, async () => {
    const fixture = managedDatabaseFixture(target)
    vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
      const path = String(input)
      return response(
        managedWire(
          path.endsWith("/projects")
            ? { projects: [] }
            : path.endsWith("/providers")
              ? { providers: [fixture.provider] }
              : { connections: [fixture.connection] }
        )
      )
    })
    mount()
    expect(
      await screen.findByRole("button", { name: "Deploy PostgreSQL" })
    ).toBeEnabled()
    expect(
      screen.getByRole("combobox", { name: "Deployment target" })
    ).toHaveTextContent("Selected account")
    expect(screen.getByText(fixture.profile.computeClass)).toBeInTheDocument()
    expect(
      screen.getByText(new RegExp(`${fixture.profile.storageGb} GiB`))
    ).toBeInTheDocument()
    expect(
      screen.getByText(target === "rds" ? "7 days" : "7 copies")
    ).toBeInTheDocument()
    expect(
      screen.queryByRole("checkbox", { name: "Daily backups" })
    ).not.toBeInTheDocument()
    expect(screen.getByText(/Review provider charges/)).toBeInTheDocument()
    if (target === "rds") {
      expect(screen.getByRole("textbox", { name: "Region" })).toHaveValue(
        "us-east-1"
      )
      expect(screen.getByRole("textbox", { name: "Region" })).toHaveAttribute(
        "readonly"
      )
    } else {
      await userEvent.click(screen.getByRole("combobox", { name: "Region" }))
      await userEvent.click(
        await screen.findByRole("option", { name: "europe-west1" })
      )
      expect(
        screen.getByRole("combobox", { name: "Region" })
      ).toHaveTextContent("europe-west1")
    }
    await userEvent.click(
      screen.getByRole("combobox", { name: "Database engine" })
    )
    await userEvent.click(
      await screen.findByRole("option", { name: "MySQL 8.4" })
    )
    expect(screen.getByText("Connect a Docker server")).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Deploy MySQL" })
    ).not.toBeInTheDocument()
  })
}

it("keeps a failed provider read distinct from having no eligible cloud accounts", async () => {
  const fixture = managedDatabaseFixture("rds")
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    return path.endsWith("/providers")
      ? response({ error: "Cloud profiles denied" }, 403)
      : response(
          managedWire(
            path.endsWith("/projects")
              ? { projects: [] }
              : { connections: [fixture.connection] }
          )
        )
  })
  mount()
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Cloud profiles denied"
  )
  expect(
    screen.queryByText("Connect a database target")
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Deploy PostgreSQL" })
  ).not.toBeInTheDocument()
})

it("identifies managed recovery as pending instead of offering container backups", async () => {
  const fixture = managedDatabaseFixture("rds")
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.endsWith("/providers"))
      return response(managedWire({ providers: [fixture.provider] }))
    if (path.endsWith("/projects"))
      return response({ projects: [{ id: "p", name: "Managed orders" }] })
    if (path.endsWith("/environments"))
      return response({ environments: [{ id: "prod", name: "production" }] })
    if (path.includes("/bindings"))
      return response({
        bindings: [
          {
            id: "binding",
            name: "database",
            subject: "resource",
            provider: "rds",
            environment_id: "prod",
            config: {
              kind: "postgres",
              managed: { profile: { id: "postgresql-17-rds" } },
            },
          },
        ],
      })
    return response({
      connections: [],
      backups: [],
      restores: [],
      policies: [],
    })
  })
  mount()
  expect(
    await screen.findByText("Managed database recovery")
  ).toBeInTheDocument()
  expect(
    screen.queryByText("No catalog database in this environment")
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Back up now" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("heading", { name: "Isolated recovery copies" })
  ).not.toBeInTheDocument()
})
it("keeps empty projects distinct from a failed read and offers retry", async () => {
  let failed = true
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    String(input).endsWith("/projects")
      ? response(
          failed ? { error: "Projects unavailable" } : { projects: [] },
          failed ? 503 : 200
        )
      : response({ connections: [] })
  )
  mount()
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Projects unavailable"
  )
  expect(screen.queryByText("No service projects yet")).not.toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No service projects yet")).toBeInTheDocument()
  expect(screen.getByText("Connect a database target")).toBeInTheDocument()
  expect(
    screen.getByRole("combobox", { name: "Database engine" })
  ).toBeEnabled()
})
it("does not request recovery data or show deployment controls without grants", async () => {
  permissions.allowed = false
  const requests: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    requests.push(url)
    return response(
      url.endsWith("/projects")
        ? { projects: [{ id: "p", name: "Private database" }] }
        : { connections: [] }
    )
  })
  mount()
  expect(
    await screen.findByText(
      "You need service backup read access to view recovery points."
    )
  ).toBeInTheDocument()
  expect(requests.some((url) => url.includes("service-backups"))).toBe(false)
  expect(
    screen.queryByRole("button", { name: "Deploy PostgreSQL" })
  ).not.toBeInTheDocument()
})

it("selects the database engine in the shared deployment form", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    response(
      String(input).endsWith("/projects")
        ? { projects: [] }
        : {
            connections: [
              {
                id: "target",
                name: "Docker acceptance",
                cloud: "server",
                status: "ready",
                details: { architecture: "arm64" },
              },
            ],
          }
    )
  )
  mount()
  expect(
    await screen.findByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeEnabled()
  await userEvent.click(
    screen.getByRole("combobox", { name: "Database engine" })
  )
  await userEvent.click(
    await screen.findByRole("option", { name: "MySQL 8.4" })
  )
  expect(screen.getByRole("button", { name: "Deploy MySQL" })).toBeEnabled()
  expect(screen.getByLabelText("Project name")).toHaveValue("MySQL")
  for (const [engine, name] of [
    ["Redis 8.2", "Redis"],
    ["Valkey 8.1", "Valkey"],
  ]) {
    await userEvent.click(
      screen.getByRole("combobox", { name: "Database engine" })
    )
    await userEvent.click(await screen.findByRole("option", { name: engine }))
    expect(screen.getByRole("button", { name: `Deploy ${name}` })).toBeEnabled()
    expect(screen.getByLabelText("Project name")).toHaveValue(name)
  }
})

it("shows verified cache restores without claiming SQL verification", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.endsWith("/projects"))
      return response({ projects: [{ id: "p", name: "Cache" }] })
    if (path.endsWith("/environments"))
      return response({ environments: [{ id: "prod", name: "production" }] })
    if (path.includes("/bindings"))
      return response({
        bindings: [
          {
            id: "binding",
            name: "database",
            subject: "resource",
            provider: "server",
            environment_id: "prod",
            config: { kind: "redis", catalog: { id: "redis-8.2" } },
          },
        ],
      })
    if (path.endsWith("/service-backups"))
      return response({
        backups: [
          {
            id: "backup",
            name: "database",
            environment_id: "prod",
            status: "succeeded",
            bytes: 512,
            created_at: "2026-10-20T12:00:00Z",
            restored_at: "2026-10-20T12:01:00Z",
          },
        ],
      })
    if (path.endsWith("/service-restores"))
      return response({
        restores: [
          {
            id: "restore",
            backup_id: "backup",
            status: "succeeded",
            duration_ms: 1000,
            created_at: "2026-10-20T12:00:00Z",
            verified_at: "2026-10-20T12:01:00Z",
            expires_at: "2026-10-21T12:00:00Z",
          },
        ],
      })
    return response({ connections: [], policies: [] })
  })
  mount()
  expect(
    await screen.findByRole("button", { name: "Back up now" })
  ).toBeEnabled()
  expect(screen.getAllByText(/Restore verified/).length).toBeGreaterThan(0)
  expect(screen.queryByText(/SQL verified/)).not.toBeInTheDocument()
})

it("offers a checked Kubernetes PostgreSQL target and removes it for other engines", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    response(
      String(input).endsWith("/projects")
        ? { projects: [] }
        : {
            connections: [
              {
                id: "cluster",
                name: "Cluster acceptance",
                cloud: "kubernetes",
                status: "ready",
                details: {
                  architecture: "arm64",
                  namespace: "databases",
                  namespace_uid: "namespace-identity",
                  storage_class: "retained",
                  storage_class_uid: "class-identity",
                  storage_reclaim: "Retain",
                  persistent_storage: "ready",
                  postgres_recovery_permissions: "ready",
                },
              },
            ],
          }
    )
  )
  mount()
  expect(
    await screen.findByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeEnabled()
  expect(
    screen.getByRole("combobox", { name: "Deployment target" })
  ).toHaveTextContent("Cluster acceptance")
  expect(screen.getByText(/10 GiB retained PVC/)).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("combobox", { name: "Database engine" })
  )
  await userEvent.click(
    await screen.findByRole("option", { name: "MySQL 8.4" })
  )
  expect(
    screen.queryByRole("button", { name: "Deploy MySQL" })
  ).not.toBeInTheDocument()
  expect(screen.getByText("Connect a Docker server")).toBeInTheDocument()
})

it("preserves a failed service project without offering another implicit creation", async () => {
  const fixture = managedDatabaseFixture("rds")
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    return response(
      managedWire(
        path.endsWith("/providers")
          ? { providers: [fixture.provider] }
          : {
              projects: [],
              connections: [fixture.connection],
              environments: [],
              bindings: [],
              backups: [],
              restores: [],
              policies: [],
            }
      )
    )
  })
  const deploy = vi
    .spyOn(catalog, "deployDatabase")
    .mockImplementation(async (_input, _progress, created) => {
      created("partially-created")
      throw new Error("Cloud creation refused")
    })
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Deploy PostgreSQL" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Cloud creation refused"
  )
  expect(
    screen.getByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeDisabled()
  expect(
    screen.getByRole("link", { name: "Open service project" })
  ).toHaveAttribute("href", "/projects/$id")
  expect(deploy).toHaveBeenCalledTimes(1)
  await userEvent.click(screen.getByRole("button", { name: "New service" }))
  expect(screen.getByLabelText("Project name")).toHaveValue("")
  await userEvent.type(screen.getByLabelText("Project name"), "Another service")
  expect(
    screen.getByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeEnabled()
})

it("does not silently replace the default cloud account with a server after profile refresh fails", async () => {
  const fixture = managedDatabaseFixture("rds")
  let denied = false
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.endsWith("/providers"))
      return denied
        ? response({ error: "Profiles unavailable" }, 403)
        : response(managedWire({ providers: [fixture.provider] }))
    return response(
      managedWire(
        path.endsWith("/projects")
          ? { projects: [] }
          : {
              connections: [
                fixture.connection,
                {
                  ...fixture.connection,
                  id: "docker",
                  name: "Other server",
                  cloud: "server",
                  details: { architecture: "arm64" },
                },
              ],
            }
      )
    )
  })
  mount()
  expect(
    await screen.findByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeEnabled()
  expect(
    screen.getByRole("combobox", { name: "Deployment target" })
  ).toHaveTextContent("Selected account")
  denied = true
  await act(async () => {
    await useProvidersList()
      .refetch()
      .catch(() => undefined)
  })
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Profiles unavailable"
  )
  expect(
    screen.getByRole("combobox", { name: "Deployment target" })
  ).not.toHaveTextContent("Other server")
  expect(
    screen.getByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeDisabled()
})
