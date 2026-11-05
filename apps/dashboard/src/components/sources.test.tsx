import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { Repositories } from "@/components/sources"

// The workspace's permissions, as the session lists them. A project's own
// list (below) never holds repository:connect.
const may = vi.hoisted(() => ({ edit: true, connect: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) =>
      p === "repository:connect"
        ? may.connect
        : p !== "project:update" || may.edit,
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  may.edit = true
  may.connect = true
})

const project = { id: "p", name: "Shop" } as Project

const source: Record<string, unknown> = {
  id: "src_1",
  alias: "shop",
  repository: "acme/shop",
  ref: "",
  root: "",
}

function serve(
  sources: Record<string, unknown>[] = [source],
  enable?: { status: number; body: unknown },
  manifest?: Record<string, unknown>
) {
  const calls: { method: string; path: string; body?: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      calls.push({
        method,
        path: url.pathname,
        ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
      })
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), {
          headers: { "Content-Type": "application/json" },
        })
      if (method === "DELETE") return new Response(null, { status: 204 })
      if (url.pathname === "/api/v1/projects/p/manifest")
        return manifest
          ? json({ id: "r3", revision: method === "PUT" ? 4 : 3, manifest })
          : new Response(null, { status: 404 })
      // What a project's page is told: workspace-only permissions such as
      // repository:connect are not in it.
      if (url.pathname === "/api/v1/projects/p/permissions")
        return json({
          permissions: [
            "project:read",
            ...(may.edit ? ["project:update"] : []),
            "analysis:run",
          ],
        })
      if (url.pathname === "/api/v1/bitbucket/webhooks" && enable)
        return new Response(JSON.stringify(enable.body), {
          status: enable.status,
          headers: { "Content-Type": "application/json" },
        })
      if (url.pathname.endsWith("/sources")) return json({ sources })
      if (url.pathname.endsWith("/analyses")) return json({ analyses: [] })
      if (url.pathname.endsWith("/graph"))
        return json({
          project_id: "p",
          excluded: [],
          graph: {
            sources: sources.map((s) => ({ alias: s.alias })),
            services: [],
          },
          sources: [
            {
              source: { alias: "shop" },
              shown: {
                id: "a1",
                ref: "main",
                commit_sha: "3f2a1c9aaaa",
                status: "succeeded",
                created_at: new Date().toISOString(),
              },
              latest: {
                id: "a1",
                ref: "main",
                commit_sha: "3f2a1c9aaaa",
                status: "succeeded",
                created_at: new Date().toISOString(),
                finished_at: new Date().toISOString(),
              },
            },
          ],
        })
      return json({})
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <Repositories project={project} />
    </ClientProvider>
  )
  return calls
}

test("each repository says what was read from it and when", async () => {
  serve()
  const table = await screen.findByRole("table", { name: "Repositories" })
  expect(
    within(table).getByRole("link", { name: "acme/shop" })
  ).toHaveAttribute("href", "https://github.com/acme/shop")
  expect(await within(table).findByText("main @ 3f2a1c9")).toBeInTheDocument()
  expect(within(table).getByText("just now")).toBeInTheDocument()
  expect(
    within(table).getByRole("button", { name: "Analyze" })
  ).toBeInTheDocument()
})

test("removing a repository asks first, from its menu", async () => {
  const calls = serve()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "More actions for shop" })
  )
  await user.click(await screen.findByRole("menuitem", { name: "Remove shop" }))
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText("Remove shop?")).toBeInTheDocument()
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  await vi.waitFor(() =>
    expect(calls).toContainEqual({
      method: "DELETE",
      path: "/api/v1/projects/p/sources/shop",
    })
  )
})

// shop holds web, which api in the other repository needs and the edge
// gateway only routes to.
const twoRepos = {
  version: 1,
  services: {
    web: { repo: "shop", source: "apps/web" },
    api: { repo: "backend", source: "cmd/api", needs: ["web"] },
  },
  gateways: { edge: { routes: [{ path: "/", to: "web" }] } },
}
const backend = { ...source, id: "src_2", alias: "backend" }

async function openRemove() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "More actions for shop" })
  )
  await user.click(await screen.findByRole("menuitem", { name: "Remove shop" }))
  return { user, dialog: await screen.findByRole("alertdialog") }
}

test("removing a repository takes its services out of loomplane.yaml", async () => {
  const calls = serve([source, backend], undefined, twoRepos)
  const { user, dialog } = await openRemove()
  const drop = await within(dialog).findByRole("checkbox", {
    name: "Also take web out of loomplane.yaml",
  })
  expect(drop).toBeChecked()
  expect(
    within(dialog).getByText(/The edge gateway only routes to/)
  ).toBeVisible()
  expect(
    within(dialog).getByText(/the next deploy there deletes/)
  ).toBeVisible()
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  await vi.waitFor(() =>
    expect(calls.filter((c) => c.method !== "GET")).toEqual([
      { method: "DELETE", path: "/api/v1/projects/p/sources/shop" },
      {
        method: "PUT",
        path: "/api/v1/projects/p/manifest",
        body: {
          base_revision: 3,
          manifest: {
            version: 1,
            services: { api: { repo: "backend", source: "cmd/api" } },
            gateways: {},
          },
        },
      },
    ])
  )
})

test("removing a repository can leave its services in loomplane.yaml", async () => {
  const calls = serve([source, backend], undefined, twoRepos)
  const { user, dialog } = await openRemove()
  await user.click(
    await within(dialog).findByRole("checkbox", {
      name: "Also take web out of loomplane.yaml",
    })
  )
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  await vi.waitFor(() =>
    expect(calls).toContainEqual({
      method: "DELETE",
      path: "/api/v1/projects/p/sources/shop",
    })
  )
  expect(calls.some((c) => c.method === "PUT")).toBe(false)
})

test("a viewer can open a repository but not change the list", async () => {
  may.edit = false
  serve()
  await screen.findByRole("table", { name: "Repositories" })
  expect(
    screen.queryByRole("button", { name: "Add repository" })
  ).not.toBeInTheDocument()
  const user = userEvent.setup()
  await user.click(
    screen.getByRole("button", { name: "More actions for shop" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Open on GitHub" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("menuitem", { name: "Remove shop" })
  ).not.toBeInTheDocument()
})

const bitbucketSource = {
  id: "src_2",
  alias: "api",
  repository: "bitbucket.org/acme/api",
  ref: "",
  root: "",
}

test("a Bitbucket source with push re-analysis off says why and offers Turn on", async () => {
  const calls = serve(
    [
      {
        ...bitbucketSource,
        push: {
          on: false,
          reason:
            "push re-analysis needs an https public_url, so Bitbucket can reach this Loomplane",
        },
      },
    ],
    { status: 200, body: { on: true } }
  )
  const user = userEvent.setup()
  expect(
    await screen.findByText(/Push re-analysis needs an https public_url/)
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Turn on" }))
  await vi.waitFor(() =>
    expect(calls).toContainEqual({
      method: "POST",
      path: "/api/v1/bitbucket/webhooks",
      body: { repository: "bitbucket.org/acme/api" },
    })
  )
  // The list is read again, so the notice follows what the server now says.
  await vi.waitFor(() =>
    expect(
      calls.filter((c) => c.method === "GET" && c.path.endsWith("/sources"))
        .length
    ).toBeGreaterThan(1)
  )
})

test("shows why Turn on didn't work", async () => {
  serve([{ ...bitbucketSource, push: { on: false, reason: "not yet" } }], {
    status: 400,
    body: { message: "Connect the workspace first" },
  })
  const user = userEvent.setup()
  await user.click(await screen.findByRole("button", { name: "Turn on" }))
  expect(
    await screen.findByText(/Connect the workspace first/)
  ).toBeInTheDocument()
})

test("a source with push re-analysis on, or none to speak of, shows no notice", async () => {
  serve([{ ...bitbucketSource, push: { on: true } }, source])
  await screen.findByRole("table", { name: "Repositories" })
  expect(
    screen.queryByRole("button", { name: "Turn on" })
  ).not.toBeInTheDocument()
  expect(screen.queryByText(/push re-analysis/i)).not.toBeInTheDocument()
})

test("a GitHub source never shows the notice", async () => {
  serve([source])
  await screen.findByRole("table", { name: "Repositories" })
  expect(
    screen.queryByRole("button", { name: "Turn on" })
  ).not.toBeInTheDocument()
})

test("someone who can't connect repositories sees the reason without Turn on", async () => {
  may.connect = false
  serve([{ ...bitbucketSource, push: { on: false, reason: "needs https" } }])
  expect(await screen.findByText(/Needs https/)).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Turn on" })
  ).not.toBeInTheDocument()
})
