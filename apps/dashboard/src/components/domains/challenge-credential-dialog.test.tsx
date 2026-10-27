import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { ZoneOverview } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { DomainCard } from "./domain-card"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const zero = "0001-01-01T00:00:00Z"

function overview(change: Partial<ZoneOverview["zone"]> = {}): ZoneOverview {
  return {
    state: "ready",
    wildcards: false,
    hostnames: [],
    zone: {
      id: "zone",
      orgId: "org",
      name: "acme.dev",
      mode: "managed",
      provider: "cloudflare",
      connectionId: "cf-main",
      accountId: "acct",
      projectIds: [],
      proofCheckedAt: zero,
      proofExpiresAt: zero,
      proofPublishAttemptedAt: zero,
      providerZone: {
        accountId: "acct",
        id: "cfzone1",
        name: "acme.dev",
        verified: true,
      },
      recordOwnership: true,
      revision: 3,
      verifiedAt: "2026-10-01T00:00:00Z",
      ...change,
    },
  }
}

function connection(id: string, account = "acct", status = "ready") {
  return {
    id,
    cloud: "cloudflare",
    name: `Cloudflare ${id}`,
    account: `Acme ${id}`,
    status,
    details: { account_id: account },
    missing: [],
  }
}

function stub(attach: () => Response = () => Response.json({})) {
  const posts: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        posts.push({ path, body: JSON.parse(String(init.body)) })
        return attach()
      }
      if (path === "/api/v1/projects") return Response.json({ projects: [] })
      if (path === "/api/v1/connections")
        return Response.json({
          connections: [
            connection("cf-main"),
            connection("cf-dns"),
            connection("cf-elsewhere", "another"),
            connection("cf-broken", "acct", "incomplete"),
          ],
        })
      return Response.json({}, { status: 404 })
    }
  )
  return posts
}

function mount(zone: ZoneOverview) {
  render(
    <ClientProvider client={forgeClient}>
      <DomainCard zone={zone} expanded onToggle={() => undefined} />
    </ClientProvider>
  )
}

const copy =
  "Wildcard certificates need a second Cloudflare token with DNS Edit on acme.dev only. Loomplane stores it on each server and cluster that serves a wildcard for this zone, so keep its scope to this one zone."

it("offers wildcard certificates only on a Cloudflare zone without a challenge token", () => {
  stub()
  mount(overview({ challengeConnectionId: "cf-dns" }))
  expect(
    screen.queryByRole("button", { name: "Set up wildcard certificates" })
  ).toBeNull()
  cleanup()
  mount(overview({ provider: "digitalocean" }))
  expect(
    screen.queryByRole("button", { name: "Set up wildcard certificates" })
  ).toBeNull()
  cleanup()
  mount(overview())
  expect(
    screen.getByRole("button", { name: "Set up wildcard certificates" })
  ).toBeTruthy()
})

it("attaches a second Cloudflare connection on the zone's account", async () => {
  const posts = stub()
  mount(overview())
  await userEvent.click(
    screen.getByRole("button", { name: "Set up wildcard certificates" })
  )
  const dialog = await screen.findByRole("dialog")
  expect(dialog.textContent).toContain(copy)
  const group = await screen.findByRole("group", {
    name: "Cloudflare connections",
  })
  // Not the zone's own token, another account's, or one that isn't ready.
  expect(
    [...group.querySelectorAll("button")].map((b) => b.textContent)
  ).toEqual(["Cloudflare cf-dnsAcme cf-dns"])
  expect(
    screen.getByRole("button", { name: "Connect a Cloudflare token" })
  ).toBeTruthy()
  await userEvent.click(screen.getByRole("button", { name: /cf-dns/ }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(posts).toEqual([
    {
      path: "/api/v1/dns-zones/zone/challenge-credential",
      body: { connection_id: "cf-dns", revision: 3 },
    },
  ])
})

it("says why a token was refused", async () => {
  stub(() =>
    Response.json(
      {
        message:
          "cloudflare requires a token scoped to acme.dev only: infrastructure credential scope is insufficient",
      },
      { status: 403 }
    )
  )
  mount(overview())
  await userEvent.click(
    screen.getByRole("button", { name: "Set up wildcard certificates" })
  )
  await userEvent.click(await screen.findByRole("button", { name: /cf-dns/ }))
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Cloudflare requires a token scoped to acme.dev only: infrastructure credential scope is insufficient."
  )
  expect(screen.getByRole("dialog")).toBeTruthy()
})
