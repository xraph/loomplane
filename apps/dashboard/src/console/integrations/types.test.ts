import { describe, expect, it } from "vitest"

import {
  type ConsoleIntegration,
  type SignedInConnection,
  type SignInCounts,
  countOf,
  keepsSecret,
  parseCredentials,
  replacesApp,
  setupState,
  signInAgainMessage,
} from "./types"

const none: SignInCounts = {
  total: 0,
  healthy: 0,
  incomplete: 0,
  failed: 0,
  signInNeeded: 0,
  appChanged: 0,
}

function integration(over: Partial<ConsoleIntegration>): ConsoleIntegration {
  return {
    key: "digitalocean",
    name: "DigitalOcean",
    kind: "cloud",
    editable: true,
    source: "platform",
    clientId: "do-app",
    callbackUrl: "https://lp.example/integrations/oauth/callback",
    connections: none,
    organizations: 0,
    provider: { name: "DigitalOcean" },
    ...over,
  }
}

describe("setupState", () => {
  it("puts a refused app before broken sign-ins", () => {
    const broken = { ...none, total: 2, appChanged: 1 }
    expect(
      setupState(integration({ problem: "refused", connections: broken }))
    ).toBe("refused")
    expect(setupState(integration({ connections: broken }))).toBe("broken")
  })

  it("tells apps that are set from those that aren't", () => {
    expect(setupState(integration({}))).toBe("ready")
    expect(setupState(integration({ source: "config" }))).toBe("ready")
    expect(
      setupState(integration({ source: "none", clientId: undefined }))
    ).toBe("not_set_up")
    // Missing permissions isn't a broken sign-in.
    expect(
      setupState(integration({ connections: { ...none, incomplete: 3 } }))
    ).toBe("ready")
  })

  it("handles repository hosts, which have no health", () => {
    expect(
      setupState(
        integration({
          kind: "repository",
          source: "none",
          connections: undefined,
        })
      )
    ).toBe("not_set_up")
  })
})

describe("replacesApp", () => {
  const used = integration({ organizations: 2 })

  it("warns about a new client ID or a removal while sign-ins use the app", () => {
    expect(replacesApp(used, { clientId: "other" })).toBe(true)
    expect(replacesApp(used, null)).toBe(true)
  })

  it("stays quiet when only the secret changes or nothing signed in", () => {
    expect(replacesApp(used, { clientId: " do-app " })).toBe(false)
    expect(
      replacesApp(integration({ organizations: 0 }), { clientId: "other" })
    ).toBe(false)
    expect(
      replacesApp(integration({ source: "none", clientId: undefined }), {
        clientId: "x",
      })
    ).toBe(false)
  })

  it("never warns for a cloud whose grants outlive the app", () => {
    expect(replacesApp({ ...used, outlivesApp: true }, null)).toBe(false)
  })

  it("warns for a repository host by what its connections belong to", () => {
    const bitbucket = integration({
      key: "bitbucket",
      kind: "repository",
      clientId: "bb",
      connections: undefined,
    })
    expect(replacesApp(bitbucket, { clientId: "other" })).toBe(true)
    expect(replacesApp(bitbucket, { clientId: "bb" })).toBe(false)
    const github = integration({
      key: "github",
      kind: "repository",
      clientId: "Iv1",
      appId: 42,
    })
    // A GitHub App's installations belong to its App ID, not its client.
    expect(replacesApp(github, { clientId: "Iv2", appId: "42" })).toBe(false)
    expect(replacesApp(github, { clientId: "Iv1", appId: "43" })).toBe(true)
    // Configuration's app can't be replaced here at all.
    expect(
      replacesApp({ ...bitbucket, source: "config" }, { clientId: "x" })
    ).toBe(false)
  })
})

describe("keepsSecret", () => {
  const github = integration({
    key: "github",
    kind: "repository",
    clientId: "Iv1",
    appId: 42,
    savedSecrets: ["clientSecret", "privateKey", "webhookSecret"],
  })

  it("keeps a secret only with what it belongs to", () => {
    const same = { clientId: "Iv1", appId: "42" }
    expect(keepsSecret(github, "clientSecret", same)).toBe(true)
    expect(keepsSecret(github, "privateKey", same)).toBe(true)
    expect(
      keepsSecret(github, "clientSecret", { clientId: "Iv2", appId: "42" })
    ).toBe(false)
    expect(
      keepsSecret(github, "privateKey", { clientId: "Iv1", appId: "43" })
    ).toBe(false)
    expect(
      keepsSecret(github, "webhookSecret", { clientId: "Iv2", appId: "43" })
    ).toBe(true)
  })

  it("keeps nothing that wasn't saved here", () => {
    expect(
      keepsSecret({ ...github, savedSecrets: [] }, "clientSecret", {
        clientId: "Iv1",
      })
    ).toBe(false)
    expect(
      keepsSecret({ ...github, source: "config" }, "clientSecret", {
        clientId: "Iv1",
      })
    ).toBe(false)
  })
})

describe("countOf", () => {
  const c = {
    ...none,
    total: 9,
    healthy: 3,
    failed: 1,
    signInNeeded: 2,
    appChanged: 3,
  }

  it("counts each health and the problems", () => {
    expect(countOf(c, "")).toBe(9)
    expect(countOf(c, "problems")).toBe(6)
    expect(countOf(c, "sign_in_needed")).toBe(2)
    expect(countOf(c, "app_changed")).toBe(3)
    expect(countOf(c, "healthy")).toBe(3)
    expect(countOf(undefined, "problems")).toBe(0)
  })
})

describe("parseCredentials", () => {
  it("reads Google's web client file and flat JSON", () => {
    expect(
      parseCredentials('{"web":{"client_id":"id","client_secret":"s"}}')
    ).toEqual({ clientId: "id", clientSecret: "s" })
    expect(parseCredentials('{"client_id":"id"}')).toEqual({
      clientId: "id",
      clientSecret: undefined,
    })
  })

  it("leaves a plain client ID alone", () => {
    expect(parseCredentials("1234.apps.googleusercontent.com")).toBeNull()
    expect(parseCredentials("{not json")).toBeNull()
    expect(parseCredentials('{"web":{}}')).toBeNull()
  })
})

describe("signInAgainMessage", () => {
  const conn: SignedInConnection = {
    id: "conn_1",
    orgId: "org_a",
    orgName: "Acme",
    cloud: "digitalocean",
    name: "prod",
    account: "Acme team",
    health: "app_changed",
    status: "ready",
    createdAt: "2026-11-01T00:00:00Z",
    updatedAt: "2026-11-01T00:00:00Z",
  }

  it("links the connection's page and says what to do", () => {
    const msg = signInAgainMessage(conn, "DigitalOcean", "https://lp.example")
    expect(msg).toContain('"prod" (Acme team) in Acme')
    expect(msg).toContain("https://lp.example/clouds/accounts/conn_1")
    expect(msg).toContain("Sign in again")
  })

  it("quotes a failed check's message", () => {
    const msg = signInAgainMessage(
      { ...conn, health: "failed", message: "403 from the API" },
      "DigitalOcean",
      ""
    )
    expect(msg).toContain("failed its last check: 403 from the API")
    expect(msg).toContain("the connection's page in Loomplane")
  })
})
