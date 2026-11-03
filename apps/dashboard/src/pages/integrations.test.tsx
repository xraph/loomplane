import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Integrations } from "@/pages/integrations"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => vi.fn(),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  assign.mockReset()
})

type Sent = { method: string; path: string; body: Record<string, unknown> }

const assign = vi.fn()
const gitlabHost = {
  id: "h1",
  host: "gitlab.com",
  web_url: "https://gitlab.com",
  client_id: "app",
  instance: true,
}
const selfManaged = {
  id: "h2",
  host: "git.acme.dev",
  web_url: "https://git.acme.dev",
  client_id: "app2",
  instance: false,
}
const gitlabConnection = {
  id: "c1",
  host: "gitlab.com",
  username: "rex",
  avatar_url: "",
  created_at: "2026-10-20T00:00:00Z",
  lapsed: false,
  version: "17.4.0",
  version_supported: true,
  webhook_set: false,
  webhook_url: "https://loomplane.example/api/v1/gitlab/webhooks/c1",
}
const noGitlab = {
  callback_url: "https://loomplane.example/integrations/gitlab/callback",
  can_connect: true,
  connections: [] as (typeof gitlabConnection)[],
  hosts: [] as (typeof gitlabHost)[],
  organization_hosts: false,
}

function fixture({
  installed = true,
  gitlab = noGitlab,
  sent = [] as Sent[],
  reply = {} as Record<string, { status: number; body: unknown }>,
} = {}) {
  vi.stubGlobal("location", { ...window.location, assign })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      if (method !== "GET") {
        sent.push({
          method,
          path,
          body: JSON.parse(String(init?.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        const r = reply[`${method} ${path}`]
        if (r) return Response.json(r.body, { status: r.status })
        return Response.json({})
      }
      let body: unknown = {}
      if (path === "/api/v1/gitlab") body = gitlab
      if (path === "/api/v1/github")
        body = {
          configured: true,
          install_url: "https://github.com/apps/loomplane/installations/new",
          installations: installed
            ? [
                {
                  id: "1",
                  account: "acme",
                  account_type: "Organization",
                  avatar_url: "",
                  created_at: "2026-10-20T00:00:00Z",
                  manage_url: "https://github.com/settings/installations/1",
                },
              ]
            : [],
        }
      if (path === "/api/v1/github/repositories")
        body = {
          repositories: [
            {
              full_name: "acme/web",
              default_branch: "main",
              installation: "1",
              private: true,
              url: "https://github.com/acme/web",
            },
            {
              full_name: "acme/docs",
              default_branch: "main",
              installation: "1",
              private: false,
              url: "https://github.com/acme/docs",
            },
          ],
        }
      if (path === "/api/v1/projects")
        body = {
          projects: [{ id: "p1", name: "Web app", repositories: ["acme/web"] }],
        }
      if (path === "/api/v1/connection-clouds")
        body = {
          clouds: [
            { cloud: "vercel", title: "Vercel", fields: [] },
            { cloud: "render", title: "Render", fields: [] },
          ],
        }
      if (path === "/api/v1/connections")
        body = {
          connections: [
            {
              id: "v",
              name: "Vercel team",
              cloud: "vercel",
              account: "acme-team",
              status: "ready",
            },
          ],
        }
      return Response.json(body)
    }
  )
}

const show = () =>
  render(
    <ClientProvider client={forgeClient}>
      <Integrations tab="connected" onTab={() => {}} />
    </ClientProvider>
  )

it(
  "sums up what's connected and lists each GitHub repository's project",
  {
    timeout: 20_000,
  },
  async () => {
    fixture()
    show()
    expect(
      await screen.findByText("1 GitHub account and 1 cloud account")
    ).toBeInTheDocument()
    expect(screen.getByText("1 of 1")).toBeInTheDocument()
    expect(
      await screen.findByText("Loomplane can read, 1 private")
    ).toBeInTheDocument()

    const github = screen.getByRole("region", { name: "GitHub" })
    expect(within(github).getByText("acme")).toBeInTheDocument()
    expect(within(github).getByText("In Web app")).toBeInTheDocument()
    expect(within(github).getByText("Not in a project")).toBeInTheDocument()
    expect(
      within(github).getByRole("link", { name: "Open project" })
    ).toBeInTheDocument()

    await userEvent.type(
      within(github).getByRole("searchbox", { name: "Search repositories" }),
      "docs"
    )
    expect(within(github).queryByText("acme/web")).not.toBeInTheDocument()
    expect(within(github).getByText("acme/docs")).toBeInTheDocument()
  }
)

it("puts connected clouds first and offers Connect for the rest", async () => {
  fixture()
  show()
  const clouds = await screen.findByRole("region", { name: "Clouds" })
  expect(await within(clouds).findByText("Vercel team")).toBeInTheDocument()
  expect(within(clouds).getByText("Ready")).toBeInTheDocument()
  expect(
    within(clouds).getByRole("button", { name: "Add" })
  ).toBeInTheDocument()
  expect(
    within(clouds).getByRole("button", { name: "Connect" })
  ).toBeInTheDocument()
  expect(within(clouds).getByText("Not connected.")).toBeInTheDocument()
  // The logo tile carries the name too, so read the order from the rows.
  const rows = within(clouds)
    .getAllByRole("listitem")
    .map((row) => row.textContent)
  expect(rows[0]).toMatch(/^Vercel/)
  expect(rows[1]).toMatch(/^Render/)
})

it("asks for an install when no GitHub account is connected", async () => {
  fixture({ installed: false })
  show()
  expect(await screen.findByText("No GitHub accounts yet")).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: /Install on GitHub/ })
  ).toHaveAttribute(
    "href",
    "https://github.com/apps/loomplane/installations/new"
  )
})

/* ------------------------------------------------------------------------ */
/* GitLab                                                                     */

const gitlabCard = () => screen.getByRole("region", { name: "GitLab" })

it("says what works without GitLab set up", async () => {
  fixture()
  show()
  expect(
    await within(gitlabCard()).findByText(
      "Public gitlab.com projects can still be read. Ask your operator to set up GitLab to read private ones."
    )
  ).toBeInTheDocument()
  expect(
    within(gitlabCard()).queryByRole("button", { name: /Connect/ })
  ).not.toBeInTheDocument()
})

it("offers GitLab.com, each other host and a self-managed one", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/sign-ins": {
        status: 200,
        body: { authorize_url: "https://gitlab.com/oauth/authorize?x=1" },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost, selfManaged],
      organization_hosts: true,
    },
  })
  show()
  const card = gitlabCard()
  expect(
    await within(card).findByRole("button", { name: "Connect GitLab.com" })
  ).toBeInTheDocument()
  expect(within(card).getByText("git.acme.dev")).toBeInTheDocument()
  expect(
    within(card).getByRole("button", { name: "Connect" })
  ).toBeInTheDocument()
  expect(
    within(card).getByRole("button", { name: "Add a self-managed GitLab" })
  ).toBeInTheDocument()

  await userEvent.click(
    within(card).getByRole("button", { name: "Connect GitLab.com" })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith(
      "https://gitlab.com/oauth/authorize?x=1"
    )
  )
  expect(sent).toContainEqual({
    method: "POST",
    path: "/api/v1/gitlab/sign-ins",
    body: { host_id: "h1" },
  })
})

it("starts the sign-in for another host", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/sign-ins": {
        status: 200,
        body: { authorize_url: "https://git.acme.dev/oauth/authorize" },
      },
    },
    gitlab: { ...noGitlab, hosts: [selfManaged] },
  })
  show()
  await userEvent.click(
    await within(gitlabCard()).findByRole("button", { name: "Connect" })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith("https://git.acme.dev/oauth/authorize")
  )
  expect(sent[0]?.body).toEqual({ host_id: "h2" })
})

it("keeps the self-managed option for people who may connect", async () => {
  fixture({
    gitlab: {
      ...noGitlab,
      can_connect: false,
      hosts: [gitlabHost],
      organization_hosts: true,
    },
  })
  show()
  await within(gitlabCard()).findByRole("button", {
    name: "Connect GitLab.com",
  })
  expect(
    within(gitlabCard()).queryByRole("button", {
      name: "Add a self-managed GitLab",
    })
  ).not.toBeInTheDocument()
})

it("shows a connection and counts it in the summary", async () => {
  fixture({
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [
        { ...gitlabConnection, version: "16.0.1", version_supported: false },
      ],
    },
  })
  show()
  expect(
    await screen.findByText(
      "1 GitHub account, 1 GitLab account and 1 cloud account"
    )
  ).toBeInTheDocument()
  const card = gitlabCard()
  expect(await within(card).findByText("rex")).toBeInTheDocument()
  expect(within(card).getByText("gitlab.com")).toBeInTheDocument()
  expect(
    within(card).getByText(
      "Reads what this GitLab user can see. A dedicated GitLab user with Reporter access is safest."
    )
  ).toBeInTheDocument()
  expect(
    within(card).getByText(
      "GitLab 16.0.1 is older than Loomplane supports; some things may not work"
    )
  ).toBeInTheDocument()
  expect(
    within(card).queryByRole("button", { name: "Sign in again" })
  ).not.toBeInTheDocument()
  expect(
    within(card).queryByRole("button", { name: "Connect GitLab.com" })
  ).not.toBeInTheDocument()
})

it("asks to sign in again when the sign-in lapsed", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/sign-ins": {
        status: 200,
        body: { authorize_url: "https://gitlab.com/oauth/authorize?again=1" },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [{ ...gitlabConnection, lapsed: true }],
    },
  })
  show()
  await userEvent.click(
    await within(gitlabCard()).findByRole("button", { name: "Sign in again" })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith(
      "https://gitlab.com/oauth/authorize?again=1"
    )
  )
  expect(sent[0]?.body).toEqual({ host_id: "h1" })
  expect(
    within(gitlabCard()).queryByText(/is older than Loomplane supports/)
  ).not.toBeInTheDocument()
})

it("disconnects only after a confirm", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [gitlabConnection],
    },
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(gitlabCard()).findByRole("button", { name: "Disconnect" })
  )
  expect(sent).toEqual([])
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Disconnect" }))
  await waitFor(() =>
    expect(sent).toContainEqual({
      method: "DELETE",
      path: "/api/v1/gitlab/connections/c1",
      body: {},
    })
  )
})

it("adds a self-managed GitLab and shows the server's refusal", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/hosts": {
        status: 400,
        body: { message: "That GitLab couldn't be reached" },
      },
    },
    gitlab: { ...noGitlab, hosts: [gitlabHost], organization_hosts: true },
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(gitlabCard()).findByRole("button", {
      name: "Add a self-managed GitLab",
    })
  )
  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog).getByText(
      "https://loomplane.example/integrations/gitlab/callback"
    )
  ).toBeInTheDocument()
  expect(
    within(dialog).getByText(
      "Create an application on your GitLab with this callback URL and only the read_api scope."
    )
  ).toBeInTheDocument()
  await user.type(
    within(dialog).getByLabelText("GitLab URL"),
    "https://git.acme.dev"
  )
  await user.type(within(dialog).getByLabelText("Application ID"), "abc")
  await user.type(within(dialog).getByLabelText("Secret"), "shh")
  await user.click(within(dialog).getByRole("button", { name: "Add GitLab" }))
  expect(
    await within(dialog).findByText("That GitLab couldn't be reached.")
  ).toBeInTheDocument()
  expect(sent).toContainEqual({
    method: "POST",
    path: "/api/v1/gitlab/hosts",
    body: {
      web_url: "https://git.acme.dev",
      client_id: "abc",
      client_secret: "shh",
    },
  })
})

it("copies the callback URL", async () => {
  fixture({
    gitlab: { ...noGitlab, hosts: [gitlabHost], organization_hosts: true },
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(gitlabCard()).findByRole("button", {
      name: "Add a self-managed GitLab",
    })
  )
  await user.click(
    await screen.findByRole("button", { name: "Copy callback URL" })
  )
  expect(await navigator.clipboard.readText()).toBe(
    "https://loomplane.example/integrations/gitlab/callback"
  )
})

it("shows a webhook secret once, then drops it", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/connections/c1/webhook": {
        status: 200,
        body: {
          url: gitlabConnection.webhook_url,
          signing_token: "whsec_sign123",
          secret_token: "plain-secret-456",
        },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [gitlabConnection],
    },
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(gitlabCard()).findByRole("button", { name: "Webhook" })
  )
  expect(screen.getByText(gitlabConnection.webhook_url)).toBeInTheDocument()
  expect(
    screen.getByText(
      "Add a webhook with Push events on the project or its group, and paste this URL and one of these secrets. Use the signing token on GitLab 19 and newer."
    )
  ).toBeInTheDocument()
  await user.type(
    screen.getByLabelText("Paste GitLab's signing token"),
    "whsec_theirs"
  )
  await user.click(screen.getByRole("button", { name: "Create secret" }))
  expect(await screen.findByText("whsec_sign123")).toBeInTheDocument()
  expect(screen.getByText("plain-secret-456")).toBeInTheDocument()
  expect(sent).toContainEqual({
    method: "POST",
    path: "/api/v1/gitlab/connections/c1/webhook",
    body: { signing_token: "whsec_theirs" },
  })

  // Closing the panel drops them: opening it again shows neither.
  await user.click(screen.getByRole("button", { name: "Webhook" }))
  await user.click(screen.getByRole("button", { name: "Webhook" }))
  expect(screen.queryByText("whsec_sign123")).not.toBeInTheDocument()
  expect(screen.queryByText("plain-secret-456")).not.toBeInTheDocument()
  expect(window.localStorage.length).toBe(0)
})

it("offers to replace a secret that is already set", async () => {
  fixture({
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [{ ...gitlabConnection, webhook_set: true }],
    },
  })
  show()
  await userEvent.click(
    await within(gitlabCard()).findByRole("button", { name: "Webhook" })
  )
  expect(
    screen.getByRole("button", { name: "Replace secret" })
  ).toBeInTheDocument()
})
