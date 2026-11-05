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

function serve(
  environments: Environment[] | undefined,
  runs: Run[] = [],
  props: { mayManage?: boolean; onOpen?: () => void } = {}
) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    const environment = url.searchParams.get("environment") ?? ""
    const body = url.pathname.endsWith("/bindings")
      ? { bindings: environment === "production" ? [api] : [] }
      : { members: [] }
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
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
