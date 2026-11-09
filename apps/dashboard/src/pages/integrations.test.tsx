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

// Whether the signed-in role may do anything: false is a viewer.
let allowed = true
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => allowed }),
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
  allowed = true
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

const bitbucketWorkspace = {
  id: "w1",
  workspace: "acme",
  name: "Acme Inc",
  avatar_url: "",
  manage_url: "https://bitbucket.org/acme/workspace/settings",
  connected_by: "Rex Raphael",
  status: "ready",
  message: "",
  created_at: "2026-11-02T00:00:00Z",
}
const noBitbucket = {
  configured: false,
  can_connect: true,
  problems: [] as string[],
  workspaces: [] as (typeof bitbucketWorkspace)[],
}

function fixture({
  installed = true,
  gitlab = noGitlab,
  bitbucket = noBitbucket,
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
      if (path === "/api/v1/bitbucket") body = bitbucket
      if (path === "/api/v1/bitbucket/repositories")
        body = { repositories: [], problems: [] }
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
    within(card).getByRole("button", { name: "Connect git.acme.dev" })
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
    await within(gitlabCard()).findByRole("button", {
      name: "Connect git.acme.dev",
    })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith("https://git.acme.dev/oauth/authorize")
  )
  expect(sent[0]?.body).toEqual({ host_id: "h2" })
})

it("offers nothing to connect when the server says no", async () => {
  fixture({
    gitlab: {
      ...noGitlab,
      can_connect: false,
      hosts: [gitlabHost],
      organization_hosts: true,
    },
  })
  show()
  expect(
    await within(gitlabCard()).findByText("An admin can connect a GitLab.")
  ).toBeInTheDocument()
  expect(
    within(gitlabCard()).queryByRole("button", { name: /Connect/ })
  ).not.toBeInTheDocument()
  expect(
    within(gitlabCard()).queryByRole("button", {
      name: "Add a self-managed GitLab",
    })
  ).not.toBeInTheDocument()
})

it("gives a viewer no way to change anything", async () => {
  allowed = false
  fixture({
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost, selfManaged],
      connections: [{ ...gitlabConnection, lapsed: true }],
      organization_hosts: true,
    },
  })
  show()
  const card = gitlabCard()
  expect(await within(card).findByText("rex")).toBeInTheDocument()
  expect(within(card).getByText("git.acme.dev")).toBeInTheDocument()
  expect(within(card).queryAllByRole("button")).toEqual([])
})

it("says when it can't read a GitLab's version", async () => {
  fixture({
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [
        { ...gitlabConnection, version: "", version_supported: false },
      ],
    },
  })
  show()
  expect(
    await screen.findByText("Loomplane couldn't read this GitLab's version")
  ).toBeInTheDocument()
  expect(screen.queryByText(/is older than Loomplane supports/)).toBeNull()
})

it("keeps listing connections after the operator's config is gone", async () => {
  fixture({ gitlab: { ...noGitlab, connections: [gitlabConnection] } })
  show()
  const card = gitlabCard()
  expect(await within(card).findByText("rex")).toBeInTheDocument()
  expect(
    within(card).getByRole("button", { name: "Disconnect rex on gitlab.com" })
  ).toBeInTheDocument()
  expect(
    within(card).queryByText(/Ask your operator to set up GitLab/)
  ).not.toBeInTheDocument()
})

it("lets someone connect another account on a host that has one", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/sign-ins": {
        status: 200,
        body: { authorize_url: "https://gitlab.com/oauth/authorize?two=1" },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [gitlabConnection],
    },
  })
  show()
  await userEvent.click(
    await within(gitlabCard()).findByRole("button", {
      name: "Connect gitlab.com",
    })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith(
      "https://gitlab.com/oauth/authorize?two=1"
    )
  )
  expect(sent[0]?.body).toEqual({ host_id: "h1" })
})

it("removes an organization's host after a confirm, and shows a refusal", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "DELETE /api/v1/gitlab/hosts/h2": {
        status: 409,
        body: { message: "Someone is still connected through it" },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost, selfManaged],
      connections: [gitlabConnection],
      organization_hosts: true,
    },
  })
  show()
  const user = userEvent.setup()
  const card = gitlabCard()
  // gitlab.com is the instance's own: nothing to remove.
  await within(card).findByRole("button", { name: "Remove git.acme.dev" })
  expect(
    within(card).queryByRole("button", { name: "Remove gitlab.com" })
  ).not.toBeInTheDocument()
  await user.click(
    within(card).getByRole("button", { name: "Remove git.acme.dev" })
  )
  expect(sent).toEqual([])
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  expect(
    await within(dialog).findByText("Someone is still connected through it.")
  ).toBeInTheDocument()
  expect(sent).toContainEqual({
    method: "DELETE",
    path: "/api/v1/gitlab/hosts/h2",
    body: {},
  })
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
  expect(within(card).getAllByText("gitlab.com").length).toBeGreaterThan(0)
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
    await within(gitlabCard()).findByRole("button", {
      name: "Sign in again as rex on gitlab.com",
    })
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
    await within(gitlabCard()).findByRole("button", {
      name: "Disconnect rex on gitlab.com",
    })
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
      "Create a confidential application on your GitLab with this callback URL and only the read_api scope."
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

it("shows a created secret token once, then drops it", async () => {
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
    await within(gitlabCard()).findByRole("button", {
      name: "Webhook for rex on gitlab.com",
    })
  )
  expect(screen.getByText(gitlabConnection.webhook_url)).toBeInTheDocument()
  expect(
    screen.getByText(
      "In GitLab, add a webhook with Push events on the project or its group, and paste this URL."
    )
  ).toBeInTheDocument()
  expect(
    screen.getByText(
      "On older GitLab, create a secret here and paste it into GitLab's Secret token field."
    )
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Create secret" }))
  expect(await screen.findByText("plain-secret-456")).toBeInTheDocument()
  // Loomplane's signing token is never offered: GitLab makes its own.
  expect(screen.queryByText("whsec_sign123")).not.toBeInTheDocument()
  expect(screen.queryByText("Signing token")).not.toBeInTheDocument()
  expect(sent).toContainEqual({
    method: "POST",
    path: "/api/v1/gitlab/connections/c1/webhook",
    body: {},
  })

  // Closing the panel drops it: opening it again doesn't show it.
  await user.click(
    screen.getByRole("button", { name: "Webhook for rex on gitlab.com" })
  )
  await user.click(
    screen.getByRole("button", { name: "Webhook for rex on gitlab.com" })
  )
  expect(screen.queryByText("plain-secret-456")).not.toBeInTheDocument()
  expect(window.localStorage.length).toBe(0)
})

it("saves GitLab's signing token and shows no secret back", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/connections/c1/webhook": {
        status: 200,
        body: {
          url: gitlabConnection.webhook_url,
          signing_token: "whsec_theirs",
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
    await within(gitlabCard()).findByRole("button", {
      name: "Webhook for rex on gitlab.com",
    })
  )
  expect(
    screen.getByText(
      "On GitLab 19 and newer, choose Generate signing token in GitLab, copy it and paste it here. Loomplane keeps it."
    )
  ).toBeInTheDocument()
  const save = screen.getByRole("button", { name: "Save signing token" })
  // Nothing to save until a token is pasted.
  expect(save).toBeDisabled()
  await user.type(
    screen.getByLabelText("Paste GitLab's signing token"),
    "  whsec_theirs "
  )
  await user.click(save)
  expect(
    await screen.findByText(
      "Saved. Loomplane checks GitLab's signature on every delivery."
    )
  ).toBeInTheDocument()
  expect(sent).toContainEqual({
    method: "POST",
    path: "/api/v1/gitlab/connections/c1/webhook",
    body: { signing_token: "whsec_theirs" },
  })
  expect(screen.queryByText("whsec_theirs")).not.toBeInTheDocument()
  expect(screen.queryByText("plain-secret-456")).not.toBeInTheDocument()
  expect(screen.getByLabelText("Paste GitLab's signing token")).toHaveValue("")
})

it("asks before replacing a secret that is already set", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/gitlab/connections/c1/webhook": {
        status: 200,
        body: {
          url: gitlabConnection.webhook_url,
          signing_token: "new-signing",
          secret_token: "new-secret",
        },
      },
    },
    gitlab: {
      ...noGitlab,
      hosts: [gitlabHost],
      connections: [{ ...gitlabConnection, webhook_set: true }],
    },
  })
  show()
  await userEvent.click(
    await within(gitlabCard()).findByRole("button", {
      name: "Webhook for rex on gitlab.com",
    })
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole("button", { name: "Replace secret" }))
  // It breaks the hook already in GitLab, so it asks first.
  const dialog = await screen.findByRole("alertdialog")
  expect(
    within(dialog).getByText(
      "Loomplane forgets the current secret, so the webhook already set up in GitLab stops working until it uses the new one."
    )
  ).toBeInTheDocument()
  expect(sent).toEqual([])
  await user.click(within(dialog).getByRole("button", { name: "Replace it" }))
  expect(await screen.findByText("new-secret")).toBeInTheDocument()
  expect(screen.queryByText("new-signing")).not.toBeInTheDocument()
  expect(sent).toHaveLength(1)

  // Saving a pasted signing token over it asks too.
  await user.type(
    screen.getByLabelText("Paste GitLab's signing token"),
    "whsec_new"
  )
  await user.click(screen.getByRole("button", { name: "Save signing token" }))
  const again = await screen.findByRole("alertdialog")
  await user.click(within(again).getByRole("button", { name: "Keep it" }))
  expect(sent).toHaveLength(1)
})

/* ------------------------------------------------------------------------ */
/* Bitbucket                                                                  */

const bitbucketCard = () => screen.getByRole("region", { name: "Bitbucket" })
const configured = { ...noBitbucket, configured: true }

it("says what works without a Bitbucket OAuth consumer", async () => {
  fixture()
  show()
  const card = bitbucketCard()
  expect(
    await within(card).findByText(
      /Only public repositories: no Bitbucket OAuth consumer is set up/
    )
  ).toBeInTheDocument()
  expect(
    within(card).getByRole("link", { name: "Bitbucket guide" })
  ).toHaveAttribute(
    "href",
    "https://loomplane.dev/docs/guides/bitbucket-repositories"
  )
  expect(
    within(card).queryByRole("button", { name: /Connect/ })
  ).not.toBeInTheDocument()
})

it("lists each workspace and counts it in the summary", async () => {
  fixture({
    bitbucket: {
      ...configured,
      workspaces: [
        bitbucketWorkspace,
        { ...bitbucketWorkspace, id: "w2", workspace: "tools", name: "Tools" },
      ],
    },
  })
  show()
  expect(
    await screen.findByText(
      "1 GitHub account, 2 Bitbucket workspaces and 1 cloud account"
    )
  ).toBeInTheDocument()
  const card = bitbucketCard()
  expect(await within(card).findByText("Acme Inc")).toBeInTheDocument()
  expect(within(card).getByText("acme")).toBeInTheDocument()
  expect(within(card).getAllByText(/Connected by Rex Raphael/)).toHaveLength(2)
  expect(
    within(card).getAllByRole("link", { name: /Manage/ })[0]
  ).toHaveAttribute("href", "https://bitbucket.org/acme/workspace/settings")
  expect(
    within(card).getByRole("button", { name: "Disconnect Acme Inc" })
  ).toBeInTheDocument()
  expect(
    within(card).getByRole("button", { name: "Connect workspace" })
  ).toBeInTheDocument()
  expect(
    within(card).queryByRole("button", { name: /Reconnect/ })
  ).not.toBeInTheDocument()
})

it("offers a first workspace when none is connected", async () => {
  fixture({ bitbucket: configured })
  show()
  expect(
    await within(bitbucketCard()).findByText("No Bitbucket workspaces yet")
  ).toBeInTheDocument()
  expect(
    within(bitbucketCard()).getByRole("button", { name: "Connect workspace" })
  ).toBeInTheDocument()
})

it("shows why a workspace needs connecting again, and reconnects it", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/bitbucket/authorize": {
        status: 200,
        body: { url: "https://bitbucket.org/site/oauth2/authorize?again=1" },
      },
    },
    bitbucket: {
      ...configured,
      workspaces: [
        {
          ...bitbucketWorkspace,
          status: "reconnect_needed",
          message: "Bitbucket no longer accepts this sign-in.",
        },
      ],
    },
  })
  show()
  const card = bitbucketCard()
  expect(
    await within(card).findByText("Bitbucket no longer accepts this sign-in.")
  ).toBeInTheDocument()
  await userEvent.click(
    within(card).getByRole("button", { name: "Reconnect Acme Inc" })
  )
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith(
      "https://bitbucket.org/site/oauth2/authorize?again=1"
    )
  )
  expect(sent).toEqual([
    {
      method: "POST",
      path: "/api/v1/bitbucket/authorize",
      body: { workspace: "acme" },
    },
  ])
})

it("asks for the workspace slug, then leaves for Bitbucket", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    reply: {
      "POST /api/v1/bitbucket/authorize": {
        status: 200,
        body: { url: "https://bitbucket.org/site/oauth2/authorize?x=1" },
      },
    },
    bitbucket: configured,
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(bitbucketCard()).findByRole("button", {
      name: "Connect workspace",
    })
  )
  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog).getByText(/acme in bitbucket.org\/acme/)
  ).toBeInTheDocument()
  const connect = within(dialog).getByRole("button", {
    name: "Connect workspace",
  })
  expect(connect).toBeDisabled()
  await user.type(within(dialog).getByLabelText("Workspace slug"), " acme ")
  await user.click(connect)
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith(
      "https://bitbucket.org/site/oauth2/authorize?x=1"
    )
  )
  expect(sent).toEqual([
    {
      method: "POST",
      path: "/api/v1/bitbucket/authorize",
      body: { workspace: "acme" },
    },
  ])
})

it("shows the server's refusal in the connect dialog and stays", async () => {
  fixture({
    reply: {
      "POST /api/v1/bitbucket/authorize": {
        status: 400,
        body: { message: "That isn't a Bitbucket workspace slug" },
      },
    },
    bitbucket: configured,
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(bitbucketCard()).findByRole("button", {
      name: "Connect workspace",
    })
  )
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Workspace slug"), "no way")
  await user.click(
    within(dialog).getByRole("button", { name: "Connect workspace" })
  )
  expect(
    await within(dialog).findByText("That isn't a Bitbucket workspace slug.")
  ).toBeInTheDocument()
  expect(assign).not.toHaveBeenCalled()
})

it("disconnects a workspace only after a confirm", async () => {
  const sent: Sent[] = []
  fixture({
    sent,
    bitbucket: { ...configured, workspaces: [bitbucketWorkspace] },
  })
  show()
  const user = userEvent.setup()
  await user.click(
    await within(bitbucketCard()).findByRole("button", {
      name: "Disconnect Acme Inc",
    })
  )
  expect(sent).toEqual([])
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Disconnect" }))
  await waitFor(() =>
    expect(sent).toContainEqual({
      method: "DELETE",
      path: "/api/v1/bitbucket/workspaces/w1",
      body: {},
    })
  )
})

it("shows what the status says went wrong", async () => {
  fixture({
    bitbucket: {
      ...configured,
      problems: ["Bitbucket turned down bitbucket.client_id"],
      workspaces: [bitbucketWorkspace],
    },
  })
  show()
  expect(
    await within(bitbucketCard()).findByText(
      "Bitbucket turned down bitbucket.client_id"
    )
  ).toBeInTheDocument()
})

it("gives a viewer no way to change a workspace", async () => {
  allowed = false
  fixture({
    bitbucket: {
      ...configured,
      workspaces: [{ ...bitbucketWorkspace, status: "reconnect_needed" }],
    },
  })
  show()
  const card = bitbucketCard()
  expect(await within(card).findByText("Acme Inc")).toBeInTheDocument()
  expect(within(card).queryAllByRole("button")).toEqual([])
})

it("spins only the Reconnect that was pressed, and holds the others", async () => {
  fixture({
    reply: {},
    bitbucket: {
      ...configured,
      workspaces: [
        { ...bitbucketWorkspace, status: "reconnect_needed", message: "a" },
        {
          ...bitbucketWorkspace,
          id: "w2",
          workspace: "tools",
          name: "Tooling",
          status: "reconnect_needed",
          message: "b",
        },
      ],
    },
  })
  // The link never comes back, so the first row stays pending.
  const real = globalThis.fetch
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
    String(input).includes("/bitbucket/authorize")
      ? new Promise<Response>(() => {})
      : real(input, init)
  )
  show()
  const card = bitbucketCard()
  const first = await within(card).findByRole("button", {
    name: "Reconnect Acme Inc",
  })
  const second = within(card).getByRole("button", {
    name: "Reconnect Tooling",
  })
  await userEvent.click(first)
  await waitFor(() => expect(second).toBeDisabled())
  expect(
    first.querySelector('[role="status"], svg.animate-spin')
  ).not.toBeNull()
  expect(second.querySelector('[role="status"], svg.animate-spin')).toBeNull()
})

it("keeps listing workspaces without a consumer, with the note and no Reconnect", async () => {
  fixture({
    bitbucket: {
      ...noBitbucket,
      workspaces: [
        { ...bitbucketWorkspace, status: "reconnect_needed", message: "gone" },
      ],
    },
  })
  show()
  const card = bitbucketCard()
  expect(await within(card).findByText("Acme Inc")).toBeInTheDocument()
  expect(
    within(card).getByText(
      /Only public repositories: no Bitbucket OAuth consumer is set up/
    )
  ).toBeInTheDocument()
  expect(
    within(card).queryByRole("button", { name: /Reconnect/ })
  ).not.toBeInTheDocument()
  expect(
    within(card).queryByRole("button", { name: "Connect workspace" })
  ).not.toBeInTheDocument()
  expect(
    within(card).getByRole("button", { name: "Disconnect Acme Inc" })
  ).toBeInTheDocument()
})
