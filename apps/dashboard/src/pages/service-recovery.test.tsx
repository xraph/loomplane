import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ServiceCatalog } from "@/pages/service-catalog"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
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
})

function recoveryFixture({ removed = false, everyHours = 6, kind = "postgres" } = {}) {
  const writes: { url: string; body: Record<string, unknown> }[] = []
  let enabled = true
  let cadence = everyHours
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let body: unknown = {}
      if (init?.method && init.method !== "GET") {
        const payload = init.body ? JSON.parse(String(init.body)) : {}
        writes.push({ url, body: payload })
        if (url.endsWith("/backup-policies")) {
          enabled = payload.enabled
          cadence = payload.every_hours
        }
      }
      if (url.endsWith("/projects"))
        body = { projects: [{ id: "p", name: "Database project" }] }
      if (url.endsWith("/connections")) body = { connections: [] }
      if (url.endsWith("/environments"))
        body = {
          environments: [
            { id: "prod", name: "production" },
            { id: "preview", name: "preview" },
          ],
        }
      if (url.includes("/bindings"))
        body = {
          bindings: removed
            ? []
            : [
                {
                  id: "binding",
                  name: "database",
                  subject: "resource",
                  provider: "server",
                  environment_id: "prod",
                  config: {
                    kind,
                    provider: "server",
                    catalog: { id: "postgresql-17" },
                  },
                },
              ],
        }
      if (url.endsWith("/service-backups"))
        body = {
          backups: [
            {
              id: "backup-prod",
              name: "database",
              environment_id: "prod",
              status: "succeeded",
              bytes: 8192,
              created_at: "2026-10-18T12:00:00Z",
            },
            {
              id: "backup-preview",
              name: "database",
              environment_id: "preview",
              status: "succeeded",
              bytes: 8192,
              created_at: "2026-10-18T12:00:00Z",
            },
            {
              id: "backup-other",
              name: "another",
              environment_id: "prod",
              status: "succeeded",
              bytes: 8192,
              created_at: "2026-10-18T12:00:00Z",
            },
          ],
        }
      if (url.endsWith("/service-restores"))
        body = {
          restores: [
            {
              id: "restore-prod",
              backup_id: "backup-prod",
              status: "succeeded",
              created_at: "2026-10-18T12:00:00Z",
              expires_at: "2026-10-19T12:00:00Z",
            },
            {
              id: "restore-preview",
              backup_id: "backup-preview",
              status: "succeeded",
              created_at: "2026-10-18T12:00:00Z",
              expires_at: "2026-10-19T12:00:00Z",
            },
            {
              id: "restore-other",
              backup_id: "backup-other",
              status: "succeeded",
              created_at: "2026-10-18T12:00:00Z",
              expires_at: "2026-10-19T12:00:00Z",
            },
          ],
        }
      if (url.endsWith("/backup-policies")) {
        const policy = {
          id: "policy",
          environment: "production",
          resource: "database",
          every_hours: cadence,
          enabled,
          next_at: "2026-10-19T12:00:00Z",
        }
        body = init?.method === "PUT" ? policy : { policies: [policy] }
      }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ServiceCatalog />
    </ClientProvider>
  )
  return writes
}

it("shows only recovery copies for the selected environment and database", async () => {
  recoveryFixture()
  await screen.findByRole("button", { name: "Back up now" })
  expect(screen.getAllByRole("button", { name: "Remove copy" })).toHaveLength(1)
})

it("preserves an existing six-hour cadence when pausing the schedule", async () => {
  const writes = recoveryFixture()
  await userEvent.click(
    await screen.findByRole("button", { name: "Pause backup schedule" })
  )
  expect(writes).toContainEqual({
    url: expect.stringContaining("/backup-policies"),
    body: {
      environment: "production",
      resource: "database",
      every_hours: 6,
      enabled: false,
    },
  })
})

it("keeps restore evidence and copy cleanup available after the source is removed", async () => {
  recoveryFixture({ removed: true })
  expect(
    await screen.findByRole("button", { name: "Test restore" })
  ).toBeEnabled()
  expect(screen.getByRole("button", { name: "Remove copy" })).toBeEnabled()
  expect(
    screen.queryByRole("button", { name: "Back up now" })
  ).not.toBeInTheDocument()
})

it("requires confirmation and deletes only the selected recovery copy", async () => {
  const writes = recoveryFixture()
  await userEvent.click(
    (await screen.findAllByRole("button", { name: "Remove copy" }))[0]
  )
  const dialog = screen.getByRole("alertdialog")
  expect(writes).toHaveLength(0)
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Remove copy" })
  )
  expect(writes).toEqual([
    {
      url: expect.stringContaining("/service-restores/restore-prod"),
      body: {},
    },
  ])
})

it("saves a changed cadence without pausing an enabled schedule", async () => {
  const writes = recoveryFixture()
  const cadence = await screen.findByRole("spinbutton", {
    name: "Backup interval (hours)",
  })
  await userEvent.clear(cadence)
  await userEvent.type(cadence, "12")
  await userEvent.click(
    screen.getByRole("button", { name: "Save backup schedule" })
  )
  expect(writes).toContainEqual({
    url: expect.stringContaining("/backup-policies"),
    body: {
      environment: "production",
      resource: "database",
      every_hours: 12,
      enabled: true,
    },
  })
})

it.each(["mysql", "mariadb"])("offers recovery controls for a deployed %s recipe", async (kind) => {
  recoveryFixture({kind})
  expect(await screen.findByRole("button", {name:"Back up now"})).toBeEnabled()
  expect(screen.getByRole("button", {name:"Pause backup schedule"})).toBeEnabled()
})
