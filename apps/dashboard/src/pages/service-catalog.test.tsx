import { ClientProvider } from "@forge-go/client-react"
import { act, cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ServiceCatalog, ServiceRecovery } from "@/pages/service-catalog"
import * as catalog from "@/lib/catalog"
import type { DatabaseKind } from "@/lib/catalog-recipes"
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
function mount(kind: DatabaseKind = "postgres") {
  return render(
    <ClientProvider client={forgeClient}>
      <ServiceCatalog kind={kind} />
    </ClientProvider>
  )
}
// Recovery lives on the service's project, under Backups.
function mountRecovery(project = "p") {
  return render(
    <ClientProvider client={forgeClient}>
      <ServiceRecovery project={project} />
    </ClientProvider>
  )
}
const chosen = (name: string | RegExp) =>
  expect(screen.getByRole("radio", { name })).toHaveAttribute(
    "aria-checked",
    "true"
  )
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
    chosen(/Selected account/)
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
    // MySQL runs only on a Docker server, so the cloud account can't hold it.
    cleanup()
    mount("mysql")
    expect(
      await screen.findByText("Connect a Docker server")
    ).toBeInTheDocument()
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

it.each(["rds", "cloud-sql"] as const)(
  "identifies %s managed recovery without offering container backups",
  async (provider) => {
    const fixture = managedDatabaseFixture(provider)
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
              provider,
              environment_id: "prod",
              config: {
                kind: "postgres",
                managed: { profile: { id: `postgresql-17-${provider}` } },
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
    mountRecovery()
    expect(
      await screen.findByText("Managed database recovery")
    ).toBeInTheDocument()
    expect(
      screen.queryByText("No catalog database in this environment")
    ).not.toBeInTheDocument()
    expect(
      screen.getByText(/captured RDS and Cloud SQL history/i)
    ).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Back up now" })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("heading", { name: "Isolated recovery copies" })
    ).not.toBeInTheDocument()
  }
)
it("deploys one engine per page, with no project or recovery reads", async () => {
  const requests: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    requests.push(String(input))
    return response({ connections: [] })
  })
  mount()
  expect(
    await screen.findByText("Connect a database target")
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("combobox", { name: "Database engine" })
  ).not.toBeInTheDocument()
  expect(requests.some((url) => url.endsWith("/projects"))).toBe(false)
  expect(requests.some((url) => url.includes("service-backups"))).toBe(false)
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
    await screen.findByText(/Deployment requires project creation/)
  ).toBeInTheDocument()
  expect(requests.some((url) => url.includes("service-backups"))).toBe(false)
  expect(
    screen.queryByRole("button", { name: "Deploy PostgreSQL" })
  ).not.toBeInTheDocument()
})

it("gives each engine its own page with its name as the project name", async () => {
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
  chosen(/Docker acceptance/)
  for (const [kind, name] of [
    ["mysql", "MySQL"],
    ["redis", "Redis"],
    ["valkey", "Valkey"],
  ] as const) {
    cleanup()
    mount(kind)
    expect(
      await screen.findByRole("button", { name: `Deploy ${name}` })
    ).toBeEnabled()
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
  mountRecovery()
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
  chosen(/Cluster acceptance/)
  expect(screen.getByText(/10 GiB retained PVC/)).toBeInTheDocument()
  cleanup()
  mount("mysql")
  expect(await screen.findByText("Connect a Docker server")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Deploy MySQL" })
  ).not.toBeInTheDocument()
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
  chosen(/Selected account/)
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
    screen.queryByRole("radio", { name: /Other server/, checked: true })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeDisabled()
})

it("keeps embedded recovery scoped and opens the shared schedule and restore workflows", async () => {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (init?.method === "POST" || init?.method === "PUT") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return response({ id: "accepted" }, 202)
      }
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
            },
          ],
        })
      return response({ policies: [], restores: [] })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ServiceRecovery
        project="p"
        environment="prod"
        resource="database"
        embeddedTitle="Redis"
      />
    </ClientProvider>
  )
  expect(await screen.findByRole("heading", { name: "Redis" })).toBeVisible()
  expect(
    screen.queryByRole("combobox", { name: "Recovery environment" })
  ).toBeNull()
  expect(screen.queryByRole("button", { name: "Cloud snapshots" })).toBeNull()
  expect(screen.getByRole("columnheader", { name: "Created" })).toBeVisible()
  expect(screen.getByRole("columnheader", { name: "Size" })).toBeVisible()
  expect(screen.getByText("Not tested")).toBeVisible()
  await userEvent.click(screen.getByRole("button", { name: "Schedule" }))
  expect(screen.getByRole("dialog", { name: "Backup schedule" })).toBeVisible()
  await userEvent.click(screen.getByRole("button", { name: "Enable backups" }))
  await vi.waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].body).toEqual({
    environment: "production",
    resource: "database",
    every_hours: 24,
    enabled: true,
  })
  await userEvent.keyboard("{Escape}")
  await userEvent.click(screen.getByRole("button", { name: "Restore copy" }))
  await vi.waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[1].path).toMatch(/service-backups\/backup\/restore$/)
  expect(writes[1].body).toEqual({ request_id: expect.any(String) })
})
