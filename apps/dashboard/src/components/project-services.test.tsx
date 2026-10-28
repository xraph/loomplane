import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type {
  Environment,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ProjectServices } from "@/components/project-services"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    className,
    "aria-label": label,
  }: {
    children: React.ReactNode
    to: string
    className?: string
    "aria-label"?: string
  }) => (
    <a href={to} className={className} aria-label={label}>
      {children}
    </a>
  ),
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = {
  id: "p",
  name: "Loomplane",
  exclusions: [
    {
      kind: "service",
      match: "svc:build",
      reason: "tooling",
      keepRunning: false,
      createdBy: "ausr_rex",
      createdAt: "2026-10-24T10:00:00Z",
    },
  ],
} as unknown as Project
const environments = [
  { id: "env_prod", name: "production", kind: "production" },
  { id: "env_prev", name: "preview", kind: "preview" },
] as Environment[]

const service = (id: string, name: string, path: string) => ({
  id,
  name,
  path,
  entrypoint: path,
  kind: "http-service",
  deployable: true,
  ecosystem: "go",
  confidence: "certain",
  evidence: [],
  commands: {},
})

const view = {
  projectId: "p",
  sources: [],
  graph: {
    version: 2,
    services: [
      service("svc:loomplane", "loomplane", "cmd/loomplane"),
      service("svc:api", "api", "tests/acceptance/fixture/cmd/api"),
      service(
        "svc:worker",
        "acceptance-worker",
        "tests/acceptance/fixture/cmd/worker"
      ),
    ],
    packages: [],
    resources: [
      {
        id: "res:pg",
        kind: "postgres",
        confidence: "certain",
        needsConfirmation: false,
        evidence: [],
      },
    ],
    edges: [
      {
        from: "svc:loomplane",
        to: "res:pg",
        kind: "needs",
        confidence: "certain",
        evidence: [],
      },
    ],
    workspaces: [],
    buildSystems: [],
    notes: [],
  },
  excluded: [
    {
      service: service("svc:build", "build", "deployments/miget/build"),
      rule: { kind: "service", match: "svc:build" },
    },
  ],
} as unknown as ProjectView

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })

function serve() {
  const posted: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      if (init?.method === "POST") {
        posted.push({ path: url.pathname, body: JSON.parse(String(init.body)) })
        return json({ id: "p", name: "Loomplane", exclusions: [] })
      }
      if (url.pathname.endsWith("/domain-routes"))
        return json({
          routes: [
            {
              id: "r1",
              input: {
                service: "loomplane",
                hostname: "console.acme.dev",
                environment_id: "env_prod",
              },
              status: "active",
            },
          ],
        })
      if (url.pathname.endsWith("/bindings"))
        return json({
          bindings:
            url.searchParams.get("environment") === "production"
              ? [
                  {
                    id: "b1",
                    name: "loomplane",
                    subject: "service",
                    provider: "do-app-platform",
                    commit_sha: "e7163cd0000",
                    updated_at: "2026-10-26T18:12:00Z",
                    url: "https://loomplane-production.ondigitalocean.app",
                    drift: "",
                  },
                ]
              : [],
        })
      return json({})
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ProjectServices
        project={project}
        environments={environments}
        view={view}
      />
    </ClientProvider>
  )
  return posted
}

test("each service shows where it runs, its domains and what it uses", async () => {
  serve()
  const row = (await screen.findByRole("link", { name: "loomplane" })).closest(
    "tr"
  )!
  expect(await within(row).findByText("Live")).toBeInTheDocument()
  expect(within(row).getByText("Not deployed")).toBeInTheDocument()
  expect(await within(row).findByText("console.acme.dev")).toBeInTheDocument()
  expect(within(row).getByText("+1")).toBeInTheDocument()
  const api = screen.getByRole("link", { name: "api" }).closest("tr")!
  expect(within(api).getAllByText("Not deployed")).toHaveLength(2)
  expect(within(api).getByText("Nothing")).toBeInTheDocument()
  // The excluded build isn't listed, but the page says it's hidden.
  expect(screen.queryByRole("link", { name: "build" })).not.toBeInTheDocument()
  expect(
    screen.getByText("1 excluded service is hidden here and in the graph.")
  ).toBeInTheDocument()
})

test("filters narrow to what's deployed and what's excluded", async () => {
  serve()
  const user = userEvent.setup()
  await screen.findByText("Live")
  await user.click(screen.getByRole("radio", { name: /Deployed/ }))
  expect(screen.getByRole("link", { name: "loomplane" })).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "api" })).not.toBeInTheDocument()

  await user.click(screen.getByRole("radio", { name: /Excluded/ }))
  expect(screen.getByText("build")).toBeInTheDocument()
  expect(screen.getByText("Build or CI tooling")).toBeInTheDocument()
  expect(screen.getByText("Only this service")).toBeInTheDocument()
})

test("excluding a test fixture can take its whole directory", async () => {
  const posted = serve()
  const user = userEvent.setup()
  await screen.findByText("Live")
  await user.click(screen.getByRole("checkbox", { name: "Select api" }))
  await user.click(screen.getByRole("button", { name: "Exclude from project" }))

  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog).getByText("Exclude api from this project?")
  ).toBeInTheDocument()
  expect(
    within(dialog).getByRole("radio", { name: /Test fixture/ })
  ).toHaveAttribute("aria-checked", "true")
  expect(
    within(dialog).getByText(/Also excludes acceptance-worker/)
  ).toBeInTheDocument()
  await user.click(
    within(dialog).getByRole("button", { name: "Exclude 2 services" })
  )

  expect(posted).toEqual([
    {
      path: "/api/v1/projects/p/exclusions",
      body: {
        exclusions: [
          { kind: "path", match: "tests/acceptance/**", reason: "fixture" },
        ],
      },
    },
  ])
})

test("excluding a running service asks what happens to it", async () => {
  const posted = serve()
  const user = userEvent.setup()
  await screen.findByText("Live")
  await user.click(screen.getByRole("checkbox", { name: "Select loomplane" }))
  await user.click(screen.getByRole("button", { name: "Exclude from project" }))
  const dialog = await screen.findByRole("dialog")
  expect(within(dialog).getByText("loomplane is running")).toBeInTheDocument()
  await user.click(
    within(dialog).getByRole("radio", { name: /Leave it running/ })
  )
  await user.click(
    within(dialog).getByRole("button", { name: "Exclude loomplane" })
  )
  expect(posted[0].body).toEqual({
    exclusions: [
      {
        kind: "service",
        match: "svc:loomplane",
        name: "loomplane",
        reason: "tooling",
        keep_running: true,
      },
    ],
  })
})

test("an excluded service comes back with Include again", async () => {
  const posted = serve()
  const user = userEvent.setup()
  await screen.findByText("Live")
  await user.click(screen.getByRole("radio", { name: /Excluded/ }))
  await user.click(screen.getByRole("button", { name: "Include again" }))
  expect(posted).toEqual([
    {
      path: "/api/v1/projects/p/exclusions/remove",
      body: { exclusions: [{ kind: "service", match: "svc:build" }] },
    },
  ])
})
