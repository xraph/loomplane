import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Environment, Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { Environments } from "@/components/environment-manager"
import type { Run } from "@/lib/runs"

vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
    className,
  }: {
    children: React.ReactNode
    to: string
    params?: Record<string, string>
    className?: string
  }) => (
    <a
      href={to.replace(
        /\$(\w+)/g,
        (_, key: string) => params?.[key] ?? `$${key}`
      )}
      className={className}
    >
      {children}
    </a>
  ),
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  calls.length = 0
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = { id: "p", name: "Shop", status: "ready" } as Project
const production = {
  id: "env_prod",
  name: "production",
  kind: "production",
} as Environment
const staging = {
  id: "env_stage",
  name: "staging",
  kind: "staging",
} as Environment

const api = {
  id: "binding-api",
  name: "api",
  subject: "service",
  provider: "digitalocean",
  drift: "",
  config: {},
}

const calls: string[] = []

function serve(
  environments: Environment[] | undefined,
  runs: Run[] = [],
  props: {
    mayManage?: boolean
    onOpen?: () => void
    fallbacks?: Record<string, unknown>[]
    stagingBindings?: Record<string, unknown>[]
  } = {}
) {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const environment = url.searchParams.get("environment") ?? ""
      if (init?.method === "DELETE") {
        calls.push(`DELETE ${url.pathname}${url.search}`)
        return new Response(null, { status: 204 })
      }
      if (url.pathname.endsWith("/provider-environments/retry")) {
        calls.push(`${init?.method} ${url.pathname}`)
        return new Response(JSON.stringify({ id: "plan_new" }), {
          headers: { "Content-Type": "application/json" },
        })
      }
      if (url.pathname.endsWith("/provider-environments")) {
        calls.push(`${init?.method ?? "GET"} ${url.pathname}`)
      }
      const body = url.pathname.endsWith("/provider-environments")
        ? { provider_environments: props.fallbacks ?? [] }
        : url.pathname.endsWith("/bindings")
          ? {
              bindings:
                environment === "production"
                  ? [api]
                  : environment === "staging"
                    ? (props.stagingBindings ?? [])
                    : [],
            }
          : { members: [] }
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <Environments
        project={project}
        environments={environments}
        retry={async () => undefined}
        mayManage={props.mayManage ?? true}
        mayDeploy
        runs={runs}
        onOpen={props.onOpen ?? (() => undefined)}
      />
    </ClientProvider>
  )
}

const waitingPlan = {
  id: "plan_1",
  environment: "staging",
  status: "waiting",
  createdAt: new Date().toISOString(),
  createdBy: "u",
  projectId: "p",
} as Run

test("each environment says what it is doing and offers the next step", async () => {
  serve([production, staging], [waitingPlan])
  const table = screen.getByRole("table", { name: "Environments" })
  const [prod, stage] = within(table)
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r))
  expect(await prod.findByText("Live")).toBeInTheDocument()
  expect(await prod.findByText("DigitalOcean")).toBeInTheDocument()
  expect(prod.getByRole("link", { name: "Deploy" })).toHaveAttribute(
    "href",
    "/deployments/new"
  )
  // A plan waiting on someone is reviewed, not deployed again.
  expect(stage.getByRole("link", { name: "Review" })).toHaveAttribute(
    "href",
    "/deployments/plan_1"
  )
  expect(stage.queryByRole("link", { name: "Deploy" })).toBeNull()
  expect(stage.getByText(/1 plan to approve/)).toBeInTheDocument()
  expect(await stage.findByText("Nothing yet")).toBeInTheDocument()
})

test("the row menu opens other tabs on that environment", async () => {
  const onOpen = vi.fn()
  serve([production, staging], [], { onOpen })
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await user.click(await screen.findByRole("menuitem", { name: "Variables" }))
  expect(onOpen).toHaveBeenCalledWith("variables", "staging")
})

test("deleting an environment asks first", async () => {
  serve([production, staging])
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await user.click(
    await screen.findByRole("menuitem", { name: "Delete environment" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText("Delete staging?")).toBeInTheDocument()
  expect(
    within(dialog).getByRole("button", { name: "Delete environment" })
  ).toBeEnabled()
})

test("the only environment can't be deleted", async () => {
  serve([production])
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for production" })
  )
  expect(
    await screen.findByRole("menuitem", {
      name: "Can't delete the only environment",
    })
  ).toHaveAttribute("aria-disabled", "true")
})

test("no environments says so, with the way to add one", () => {
  serve([])
  expect(screen.getByText("No environments yet")).toBeInTheDocument()
  expect(
    screen.getAllByRole("button", { name: "Add environment" }).length
  ).toBeGreaterThan(0)
})

test("a viewer can't add environments", () => {
  serve([production], [], { mayManage: false })
  expect(
    screen.queryByRole("button", { name: "Add environment" })
  ).not.toBeInTheDocument()
})

test("managed environments open their instance instead of generic deployment or deletion", async () => {
  serve([{ ...staging, instanceId: "instance-a" }, production], [waitingPlan])
  const table = screen.getByRole("table", { name: "Environments" })
  const [managed, ordinary] = within(table)
    .getAllByRole("row")
    .slice(1)
    .map((row) => within(row))
  expect(managed.getByRole("link", { name: "View instance" })).toHaveAttribute(
    "href",
    "/projects/p/fleet/instances/instance-a"
  )
  expect(managed.queryByRole("link", { name: "Deploy" })).toBeNull()
  expect(managed.queryByRole("link", { name: "Review" })).toBeNull()
  expect(ordinary.getByRole("link", { name: "Deploy" })).toBeInTheDocument()
  await userEvent.click(
    managed.getByRole("button", { name: "More actions for staging" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Where services run" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("menuitem", { name: "Delete environment" })
  ).toBeNull()
  expect(screen.queryByRole("menuitem", { name: "Variables" })).toBeNull()
})

const vercelService = (id: string) => ({
  id,
  name: id,
  subject: "service",
  provider: "vercel",
  drift: "",
  config: {},
})

const fallback = (over: Record<string, unknown> = {}) => ({
  environment_id: "env_stage",
  provider: "vercel",
  scope: "web",
  mode: "fallback",
  permanent: false,
  reason: "Vercel needs a paid plan for custom environments",
  created_at: "2026-11-05T00:00:00Z",
  ...over,
})

test("shows a separate project note with the reason", async () => {
  serve([production, staging], [], {
    fallbacks: [fallback()],
    stagingBindings: [vercelService("web")],
  })
  const note = await screen.findByText("Separate project")
  expect(note).toHaveAttribute(
    "title",
    "web: Vercel needs a paid plan for custom environments"
  )
})

test("counts services on separate projects when mixed", async () => {
  serve([production, staging], [], {
    fallbacks: [fallback()],
    stagingBindings: [vercelService("web"), vercelService("docs")],
  })
  expect(
    await screen.findByText("1 of 2 services on separate projects")
  ).toBeInTheDocument()
})

test("hides try again when every fallback is permanent", async () => {
  const user = userEvent.setup()
  const onOpen = vi.fn()
  serve([production, staging], [], {
    fallbacks: [fallback({ permanent: true })],
    stagingBindings: [vercelService("web")],
    onOpen,
  })
  await screen.findByText("Separate project")
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await screen.findByRole("menuitem", { name: "Variables" })
  expect(
    screen.queryByRole("menuitem", { name: "Try native environment again" })
  ).toBeNull()
})

const listFallbacks = "GET /api/v1/projects/p/provider-environments"

test("try again plans a move back to native and opens deployments", async () => {
  const user = userEvent.setup()
  const onOpen = vi.fn()
  serve([production, staging], [], {
    fallbacks: [fallback()],
    stagingBindings: [vercelService("web")],
    onOpen,
  })
  await screen.findByText("Separate project")
  const listedBefore = calls.filter((c) => c === listFallbacks).length
  expect(listedBefore).toBeGreaterThan(0)
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Try native environment again",
    })
  )
  await vi.waitFor(() =>
    expect(calls).toContain(
      "POST /api/v1/projects/p/environments/env_stage/provider-environments/retry"
    )
  )
  await vi.waitFor(() =>
    expect(onOpen).toHaveBeenCalledWith("deployments", "staging")
  )
  // The cleared fallbacks are read again, so the row's note goes.
  await vi.waitFor(() =>
    expect(calls.filter((c) => c === listFallbacks).length).toBeGreaterThan(
      listedBefore
    )
  )
})

const renderBinding = (
  id: string,
  name: string,
  subject: "service" | "resource",
  alias = ""
) => ({
  id,
  name,
  alias,
  subject,
  provider: "render",
  drift: "",
  config: {},
})

const nativeStaging = [
  vercelService("web"),
  renderBinding("bnd_api", "api", "service", "shop-api-staging"),
  renderBinding("bnd_db", "db", "resource"),
]

async function openDelete() {
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await user.click(
    await screen.findByRole("menuitem", { name: "Delete environment" })
  )
  return { user, dialog: await screen.findByRole("alertdialog") }
}

test("deleting an environment on Vercel and Render lists what to keep", async () => {
  serve([production, staging], [], { stagingBindings: nativeStaging })
  const { dialog } = await openDelete()
  const keep = within(dialog)
  for (const name of ["Keep web", "Keep shop-api-staging", "Keep db"]) {
    expect(await keep.findByRole("checkbox", { name })).not.toBeChecked()
  }
  // The alias leads, with the manifest name beside it.
  expect(keep.getByText("api")).toBeInTheDocument()
  expect(
    keep.getByText(
      "Unticked databases are deleted with their data. Kept resources keep running and are listed as left behind."
    )
  ).toBeInTheDocument()
})

test("keep all databases ticks the data resources only", async () => {
  serve([production, staging], [], { stagingBindings: nativeStaging })
  const { user, dialog } = await openDelete()
  const keep = within(dialog)
  await user.click(
    await keep.findByRole("button", { name: "Keep all databases" })
  )
  expect(keep.getByRole("checkbox", { name: "Keep db" })).toBeChecked()
  expect(keep.getByRole("checkbox", { name: "Keep web" })).not.toBeChecked()
  expect(
    keep.getByRole("checkbox", { name: "Keep shop-api-staging" })
  ).not.toBeChecked()
})

test("the delete asks to keep what's ticked", async () => {
  serve([production, staging], [], { stagingBindings: nativeStaging })
  const { user, dialog } = await openDelete()
  const keep = within(dialog)
  await user.click(await keep.findByRole("checkbox", { name: "Keep db" }))
  await user.click(keep.getByRole("checkbox", { name: "Keep web" }))
  await user.click(keep.getByRole("button", { name: "Delete environment" }))
  await vi.waitFor(() =>
    expect(calls).toContain(
      "DELETE /api/v1/projects/p/environments/env_stage?keep=bnd_db&keep=web"
    )
  )
})

test("an environment with nothing on Vercel or Render deletes as before", async () => {
  serve([production, staging], [], {
    stagingBindings: [{ ...api, id: "bnd_do" }],
  })
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for staging" })
  )
  await user.click(
    await screen.findByRole("menuitem", { name: "Delete environment" })
  )
  const dialog = within(await screen.findByRole("alertdialog"))
  expect(
    dialog.getByText(/What runs there keeps running until you remove it/)
  ).toBeInTheDocument()
  expect(dialog.queryByRole("checkbox")).toBeNull()
  expect(dialog.queryByText(/Unticked databases/)).toBeNull()
  await user.click(dialog.getByRole("button", { name: "Delete environment" }))
  await vi.waitFor(() =>
    expect(calls).toContain("DELETE /api/v1/projects/p/environments/env_stage")
  )
})
