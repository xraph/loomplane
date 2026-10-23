import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"

import { OrganizationCloud } from "./org-cloud"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const cloud = {
  id: "c",
  name: "Production",
  org_id: "org",
  project_id: "",
  revision: 1,
  created_at: "2026-10-20T00:00:00Z",
  updated_at: "2026-10-20T00:00:00Z",
}
const target = (id: string, provider: string, connection: string) => ({
  id,
  name: id,
  display_name: id[0]!.toUpperCase() + id.slice(1),
  cloud_id: "c",
  provider,
  connection_id: connection,
  region: "",
  revision: 1,
})

function fixture({ granted = false, inUse = false, noTargets = false } = {}) {
  const writes: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      if (method !== "GET") {
        const sent = JSON.parse(String(init?.body ?? "{}"))
        writes.push({ method, path, body: sent })
        if (path.endsWith("/default-placement"))
          return inUse
            ? Response.json(
                { message: "an environment uses this Cloud" },
                { status: 409 }
              )
            : Response.json({
                ...cloud,
                revision: 2,
                default_placement: sent.default_placement,
              })
        return Response.json({
          cloud_id: "c",
          project_id: "web",
          target_ids: ["frontend"],
          revision: 2,
          revoked: false,
        })
      }
      let body: unknown = {}
      if (path.endsWith("/environments")) body = { environments: [] }
      if (path === "/api/v1/providers")
        body = {
          providers: [
            {
              name: "digitalocean",
              cloud: "digitalocean",
              title: "DigitalOcean App Platform",
              available: true,
              runs: ["service"],
              regions: ["nyc", "ams"],
            },
            {
              name: "vercel",
              cloud: "vercel",
              title: "Vercel",
              available: true,
              runs: ["service"],
            },
          ],
        }
      if (path === "/api/v1/clouds/c/targets" && noTargets)
        body = { targets: [] }
      else if (path === "/api/v1/clouds/c/targets")
        body = {
          targets: [
            target("frontend", "vercel", "v"),
            target("api", "digitalocean", "do-1"),
            target("worker", "digitalocean", "do-2"),
          ],
        }
      if (path === "/api/v1/clouds/c/projects")
        body = {
          grants: granted
            ? [
                {
                  cloud_id: "c",
                  project_id: "web",
                  target_ids: ["frontend", "api"],
                  revision: 1,
                  revoked: false,
                },
              ]
            : [],
        }
      if (path === "/api/v1/projects")
        body = {
          projects: [
            { id: "web", name: "Web app", icon: "globe", color: "blue" },
            { id: "jobs", name: "Jobs", icon: "box", color: "green" },
          ],
        }
      if (path === "/api/v1/connections")
        body = {
          connections: [
            {
              id: "v",
              name: "Vercel team",
              cloud: "vercel",
              account: "team",
              status: "ready",
            },
            {
              id: "do-1",
              name: "DO US",
              cloud: "digitalocean",
              account: "us",
              status: "ready",
            },
            {
              id: "do-2",
              name: "DO EU",
              cloud: "digitalocean",
              account: "eu",
              status: "ready",
            },
          ],
        }
      return Response.json(body)
    }
  )
  return { writes }
}

const show = () =>
  render(
    <ClientProvider client={forgeClient}>
      <OrganizationCloud cloud={cloud as never} />
    </ClientProvider>
  )

it("shows an organization's Cloud with the projects allowed on each target", async () => {
  fixture({ granted: true })
  show()
  expect(
    await screen.findByText("3 targets across 3 accounts on 2 providers")
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("tab", { name: "Projects" }))
  expect((await screen.findAllByText("Web app")).length).toBeGreaterThan(0)
  // Web app may use frontend and api, not worker.
  const chips = screen.getAllByText(/^(Frontend|Api|Worker)$/)
  expect(chips.length).toBeGreaterThan(0)
  expect(
    screen.getByRole("button", { name: "Edit access" })
  ).toBeInTheDocument()
})

it("gives a project access to only the targets you tick", async () => {
  const user = userEvent.setup()
  const { writes } = fixture()
  show()
  await user.click(screen.getByRole("tab", { name: "Projects" }))
  expect(
    await screen.findByText("No project uses this Cloud yet")
  ).toBeInTheDocument()
  await user.click(
    await screen.findByRole("button", { name: "Give a project access" })
  )
  await user.click(screen.getByRole("combobox", { name: "Project" }))
  await user.click(await screen.findByRole("option", { name: /Jobs/ }))
  // Every target starts ticked; leave worker out.
  await user.click(screen.getByRole("checkbox", { name: /Worker/ }))
  await user.click(screen.getByRole("button", { name: "Give access" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "PUT",
        path: "/api/v1/clouds/c/projects/jobs",
        body: { target_ids: ["frontend", "api"] },
      },
    ])
  )
})

it("disables target choices for whole Cloud access and all grant choices while saving", async () => {
  const user = userEvent.setup()
  fixture()
  const fetch = globalThis.fetch
  let finishSave!: (response: Response) => void
  const pendingSave = new Promise<Response>((resolve) => {
    finishSave = resolve
  })
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "PUT" && String(input).includes("/projects/"))
      return pendingSave
    return fetch(input, init)
  })
  show()
  await user.click(screen.getByRole("tab", { name: "Projects" }))
  await user.click(
    await screen.findByRole("button", { name: "Give a project access" })
  )
  const wholeCloud = screen.getByRole("checkbox", { name: /Whole Cloud/ })
  const frontend = screen.getByRole("checkbox", { name: /Frontend/ })
  await user.click(wholeCloud)
  expect(frontend).toHaveAttribute("aria-disabled", "true")
  await user.click(frontend)
  expect(frontend).toBeChecked()
  await user.click(wholeCloud)
  expect(frontend).not.toHaveAttribute("aria-disabled", "true")
  await user.click(screen.getByRole("button", { name: "Give access" }))
  await waitFor(() =>
    expect(wholeCloud).toHaveAttribute("aria-disabled", "true")
  )
  expect(frontend).toHaveAttribute("aria-disabled", "true")
  expect(screen.getByRole("combobox", { name: "Project" })).toBeDisabled()
  finishSave(
    Response.json({
      cloud_id: "c",
      project_id: "web",
      target_ids: ["frontend", "api", "worker"],
      revision: 2,
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
})

it("sets a targetless Cloud's one provider and account", async () => {
  const user = userEvent.setup()
  const { writes } = fixture({ noTargets: true })
  show()
  expect(
    await screen.findByText(/every service on it runs here/)
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Set default provider" }))
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO EU/ }))
  await user.click(screen.getByRole("combobox", { name: "Default region" }))
  await user.click(await screen.findByRole("option", { name: /ams/ }))
  await user.click(screen.getByRole("button", { name: "Set default" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "PUT",
        path: "/api/v1/clouds/c/default-placement",
        body: {
          default_placement: {
            provider: "digitalocean",
            connection_id: "do-2",
            region: "ams",
          },
          revision: 1,
        },
      },
    ])
  )
  expect(await screen.findByText("DO EU")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Change" })).toBeInTheDocument()
})

it("explains why a default can't move while an environment uses the Cloud", async () => {
  const user = userEvent.setup()
  fixture({ inUse: true })
  show()
  await user.click(
    await screen.findByRole("button", { name: "Set default provider" })
  )
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO US/ }))
  await user.click(screen.getByRole("button", { name: "Set default" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /Unassign the Cloud from those environments first/
  )
})

it("reviews a default provider switch through the organization Cloud change contract", async () => {
  fixture()
  const user = userEvent.setup()
  render(
    <ClientProvider client={forgeClient}>
      <OrganizationCloud
        cloud={
          {
            ...cloud,
            defaultPlacement: {
              provider: "vercel",
              connectionId: "v",
              region: "",
            },
          } as never
        }
      />
    </ClientProvider>
  )
  await user.click(await screen.findByRole("button", { name: "Change" }))
  await user.click(
    await screen.findByRole("radio", { name: /DigitalOcean App Platform/ })
  )
  await user.click(screen.getByRole("radio", { name: /DO US/ }))
  expect(
    screen.getByRole("button", { name: "Review default switch" })
  ).toBeInTheDocument()
})
