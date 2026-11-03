import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { Repositories } from "@/components/sources"

const may = vi.hoisted(() => ({ edit: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) => p !== "project:update" || may.edit,
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  may.edit = true
})

const project = { id: "p", name: "Shop" } as Project

const source = {
  id: "src_1",
  alias: "shop",
  repository: "acme/shop",
  ref: "",
  root: "",
}

function serve() {
  const calls: { method: string; path: string }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      calls.push({ method, path: url.pathname })
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), {
          headers: { "Content-Type": "application/json" },
        })
      if (method === "DELETE") return new Response(null, { status: 204 })
      if (url.pathname.endsWith("/sources")) return json({ sources: [source] })
      if (url.pathname.endsWith("/analyses")) return json({ analyses: [] })
      if (url.pathname.endsWith("/graph"))
        return json({
          project_id: "p",
          excluded: [],
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
