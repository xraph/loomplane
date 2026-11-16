import { PluginProvider, type ScopedClient } from "@forge-go/dashboard-plugin"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { ConnectionsPage } from "./pages/connections"
import { IntegrationPage } from "./pages/integration"
import { IntegrationsPage } from "./pages/integrations"
import type {
  ConsoleIntegration,
  IntegrationDetail,
  IntegrationsResponse,
  SignedInPage,
} from "./types"

const counts = {
  total: 3,
  healthy: 1,
  incomplete: 0,
  failed: 0,
  signInNeeded: 1,
  appChanged: 1,
}

const digitalocean: ConsoleIntegration = {
  key: "digitalocean",
  name: "DigitalOcean",
  kind: "cloud",
  editable: true,
  source: "platform",
  clientId: "do-app",
  savedSecrets: ["clientSecret"],
  callbackUrl: "https://lp.example/integrations/oauth/callback",
  connections: counts,
  organizations: 2,
  provider: {
    name: "DigitalOcean",
    consoleUrl: "https://cloud.digitalocean.com/account/api/applications",
    scopes: ["read", "write"],
    notes: ["Register a new application."],
  },
}

const github: ConsoleIntegration = {
  key: "github",
  name: "GitHub App",
  kind: "repository",
  editable: false,
  source: "none",
  callbackUrl: "https://lp.example/integrations/github/setup",
  organizations: 0,
  provider: { name: "GitHub" },
}

// The GitHub App as the console shows it once one is saved here.
const savedGitHub: ConsoleIntegration = {
  ...github,
  editable: true,
  source: "platform",
  clientId: "Iv1",
  appId: 42,
  slug: "lp",
  savedSecrets: ["clientSecret", "privateKey", "webhookSecret"],
  provider: {
    name: "GitHub",
    changeWarning: "Installations belong to the App.",
    fields: [
      { name: "appId", label: "App ID", kind: "number" },
      { name: "slug", label: "App slug", kind: "text" },
      { name: "privateKey", label: "Private key", kind: "pem" },
      {
        name: "webhookSecret",
        label: "Webhook secret",
        kind: "secret",
        optional: true,
      },
    ],
  },
}

const list: IntegrationsResponse = {
  appUrl: "https://lp.example",
  healthAvailable: true,
  integrations: [digitalocean, github],
  totals: counts,
}

const detail = (it: ConsoleIntegration): IntegrationDetail => ({
  ...it,
  appUrl: "https://lp.example",
  healthAvailable: true,
  envVars: ["LOOMPLANE_DIGITALOCEAN_OAUTH_CLIENT_ID"],
  configKey: "clouds.oauth.digitalocean",
})

const page: SignedInPage = {
  items: [
    {
      id: "conn_1",
      orgId: "org_a",
      orgName: "Acme",
      cloud: "digitalocean",
      name: "prod",
      health: "app_changed",
      status: "ready",
      createdAt: "2026-11-01T00:00:00Z",
      updatedAt: "2026-11-01T00:00:00Z",
    },
  ],
  total: 1,
  limit: 25,
  offset: 0,
  counts,
}

function fakeClient() {
  const query = vi.fn(
    async (intent: string, params?: Record<string, unknown>) => {
      switch (intent) {
        case "oauth.integrations":
          return list
        case "oauth.integration":
          if (params?.key === "saved-github") return detail(savedGitHub)
          return detail(params?.key === "github" ? github : digitalocean)
        case "connections.list":
          return page
      }
      throw new Error(`unexpected query ${intent}`)
    }
  )
  const command = vi.fn(async (_intent: string, payload?: unknown) => ({
    key: (payload as { key: string }).key,
  }))
  const client = {
    extension: "integrations",
    query,
    command,
  } as unknown as ScopedClient
  const wrap = ({ children }: { children: ReactNode }) => (
    <PluginProvider client={client}>{children}</PluginProvider>
  )
  return { query, command, wrap }
}

describe("integrations pages", () => {
  it("lists integrations worst first and saves an app from the side sheet", async () => {
    const { command, wrap } = fakeClient()
    render(<IntegrationsPage />, { wrapper: wrap })

    const rows = await screen.findAllByRole("row")
    // Broken sign-ins rank above a repository host that isn't set up.
    const names = rows.map((r) => r.textContent ?? "")
    expect(names.findIndex((t) => t.includes("DigitalOcean"))).toBeLessThan(
      names.findIndex((t) => t.includes("GitHub App"))
    )
    // Each row's status (the filter's options carry the same words).
    expect(names.find((t) => t.includes("DigitalOcean"))).toContain(
      "Broken sign-ins"
    )
    expect(names.find((t) => t.includes("GitHub App"))).toContain("Public only")

    fireEvent.click(screen.getByRole("button", { name: "DigitalOcean" }))
    const secret = await screen.findByLabelText("Client secret")
    // The saved secret is kept for the same client.
    expect(secret).toHaveAttribute(
      "placeholder",
      "Saved. Leave blank to keep it"
    )
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(command).toHaveBeenCalled())
    expect(command.mock.calls[0]?.[0]).toBe("oauth.save")
    expect(command.mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({ key: "digitalocean", clientId: "do-app" })
    )
  })

  it("asks before a new client ID makes people sign in again", async () => {
    const { command, wrap } = fakeClient()
    render(<IntegrationPage params={{ key: "digitalocean" }} />, {
      wrapper: wrap,
    })

    const id = await screen.findByLabelText("Client ID")
    fireEvent.change(id, { target: { value: "do-new" } })
    fireEvent.change(screen.getByLabelText("Client secret"), {
      target: { value: "s" },
    })
    expect(
      screen.getByText("Connections will have to be made again")
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(command).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole("button", { name: "Save anyway" }))
    await waitFor(() => expect(command).toHaveBeenCalled())
    expect(command.mock.calls[0]?.[0]).toBe("oauth.save")
    expect(command.mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({ clientId: "do-new", clientSecret: "s" })
    )
  })

  it("edits a GitHub App saved here, keeping its secrets", async () => {
    const { command, wrap } = fakeClient()
    render(<IntegrationPage params={{ key: "saved-github" }} />, {
      wrapper: wrap,
    })
    const key = await screen.findByLabelText("Private key")
    expect(key).toHaveAttribute("placeholder", "Saved. Leave blank to keep it")
    expect(screen.getByLabelText("App ID")).toHaveValue("42")
    expect(screen.getByLabelText("App slug")).toHaveValue("lp")
    expect(
      screen.getByLabelText("Webhook secret (optional)")
    ).toBeInTheDocument()

    // A new client secret alone saves without a warning: installations
    // belong to the App ID.
    fireEvent.change(screen.getByLabelText("Client secret"), {
      target: { value: "new-secret" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(command).toHaveBeenCalled())
    expect(command.mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({
        key: "github",
        clientId: "Iv1",
        clientSecret: "new-secret",
        appId: "42",
        slug: "lp",
        privateKey: "",
      })
    )

    // A new App ID warns, and needs that App's own key.
    fireEvent.change(screen.getByLabelText("App ID"), {
      target: { value: "43" },
    })
    expect(screen.getByLabelText("Private key")).toHaveAttribute(
      "placeholder",
      "-----BEGIN RSA PRIVATE KEY-----"
    )
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    expect(
      screen.getByText("Installations belong to the App.")
    ).toBeInTheDocument()
  })

  it("shows a repository host read-only", async () => {
    const { wrap } = fakeClient()
    render(<IntegrationPage params={{ key: "github" }} />, { wrapper: wrap })
    expect(
      await screen.findByText(/set in Loomplane's configuration/)
    ).toBeInTheDocument()
    expect(screen.queryByLabelText("Client ID")).not.toBeInTheDocument()
  })

  it("lists broken sign-ins with a message to send", async () => {
    const { query, wrap } = fakeClient()
    render(<ConnectionsPage params={{}} />, { wrapper: wrap })
    expect(await screen.findByText("prod")).toBeInTheDocument()
    expect(screen.getByText("Acme")).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: /Copy a message about prod/ })
    ).toBeInTheDocument()
    expect(query).toHaveBeenCalledWith(
      "connections.list",
      expect.objectContaining({ health: "problems", limit: 25, offset: 0 }),
      expect.anything()
    )
  })
})
