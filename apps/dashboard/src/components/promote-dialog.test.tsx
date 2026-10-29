import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { PromoteDialog } from "@/components/promote-dialog"
import { Deployments } from "@/pages/deployments"

const { navigate, allowed } = vi.hoisted(() => ({
  navigate: vi.fn(),
  allowed: { value: true },
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => allowed.value }),
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openDeploy: () => undefined }),
}))
vi.mock("@/lib/use-runs", () => ({
  useRuns: () => ({
    runs: [],
    projects: [],
    loaded: true,
    refetch: async () => undefined,
    more: () => undefined,
    loadingMore: false,
  }),
  useNow: () => Date.parse("2026-10-29T12:00:00Z"),
  usePeople: () => () => ({ name: "Ada", initials: "A" }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  navigate.mockReset()
  allowed.value = true
  forgeClient.clear()
})

const deployed = (sha: string) => ({
  sources: [
    {
      alias: "web",
      commit_sha: sha,
      deployed_at: "2026-10-29T10:00:00Z",
      plan_id: "plan_old",
      repository: "acme/web",
      source_id: "src_web",
    },
  ],
})

function serve({ staging = deployed("aaaaaaa1234567") } = {}) {
  const posts: { url: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST")
        posts.push({ url, body: JSON.parse(String(init.body)) })
      let body: unknown = {}
      if (url.endsWith("/projects"))
        body = { projects: [{ id: "p", name: "Shop" }] }
      else if (url.endsWith("/projects/p/environments"))
        body = {
          environments: [
            { id: "env_s", name: "staging" },
            { id: "env_p", name: "production" },
          ],
        }
      else if (url.endsWith("/environments/staging/deployed")) body = staging
      else if (url.endsWith("/environments/production/deployed"))
        body = deployed("9999999abcdef0")
      else if (url.endsWith("/projects/p/plans"))
        body = { id: "plan_promo", status: "planning" }
      return new Response(JSON.stringify(body), {
        status: init?.method === "POST" ? 201 : 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <PromoteDialog open onOpenChange={() => undefined} />
    </ClientProvider>
  )
  return posts
}

it("defaults to staging into production and lists what moves", async () => {
  serve()
  const list = await screen.findByRole("list", { name: "What moves" })
  const row = within(list).getByRole("listitem")
  expect(row).toHaveTextContent(/^web\s*9999999\s*aaaaaaa$/)
  // The arrow between the commits is an icon, not text.
  expect(row.querySelector("svg")).not.toBeNull()
  expect(screen.getByRole("combobox", { name: "From" })).toHaveTextContent(
    "staging"
  )
  expect(screen.getByRole("combobox", { name: "To" })).toHaveTextContent(
    "production"
  )
  expect(
    screen.getByRole("button", { name: "Promote staging to production" })
  ).toBeEnabled()
})

it("posts from and navigates to the plan", async () => {
  const posts = serve()
  await screen.findByRole("list", { name: "What moves" })
  await userEvent.click(
    screen.getByRole("button", { name: "Promote staging to production" })
  )
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_promo" },
    })
  )
  expect(posts).toEqual([
    {
      url: "/api/v1/projects/p/plans",
      body: { environment: "production", from: "staging" },
    },
  ])
})

it("explains when the source runs nothing", async () => {
  serve({ staging: { sources: [] } })
  expect(
    await screen.findByText("staging hasn't been deployed yet")
  ).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Promote staging to production" })
  ).toBeDisabled()
})

it("is hidden without deployment:create", () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response("{}", { headers: { "Content-Type": "application/json" } })
  )
  const page = () => (
    <ClientProvider client={forgeClient}>
      <Deployments status="all" />
    </ClientProvider>
  )
  const { rerender } = render(page())
  expect(screen.getByRole("button", { name: "Promote" })).toBeInTheDocument()
  allowed.value = false
  rerender(page())
  expect(
    screen.queryByRole("button", { name: "Promote" })
  ).not.toBeInTheDocument()
})
