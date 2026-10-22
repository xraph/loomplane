import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { BringDomainPanel } from "./bring-domain-panel"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => true }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const zero = "0001-01-01T00:00:00Z"

function connection(id: string, cloud: string, status = "ready") {
  return {
    id,
    cloud,
    name: `${cloud} ${id}`,
    account: `acct-${id}`,
    status,
    details: {},
    missing: [],
  }
}

function stub(
  connections: unknown[],
  create: (body: Record<string, unknown>) => Response = (body) =>
    Response.json(
      {
        id: "zone",
        org_id: "org",
        name: body.name,
        mode: body.mode,
        project_ids: [],
        proof_checked_at: zero,
        proof_expires_at: zero,
        provider_zone: {},
        record_ownership: true,
        revision: 1,
        verified_at: zero,
      },
      { status: 201 }
    )
) {
  const posts: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST" && path === "/api/v1/dns-zones") {
        const body = JSON.parse(String(init.body))
        posts.push(body)
        return create(body)
      }
      if (path === "/api/v1/connections") return Response.json({ connections })
      if (path === "/api/v1/connection-clouds")
        return Response.json({
          clouds: [
            {
              cloud: "cloudflare",
              title: "Cloudflare",
              builtin: false,
              credential: "API token",
              settings: [{ key: "account_id", label: "Account ID" }],
            },
          ],
        })
      return Response.json({}, { status: 404 })
    }
  )
  return posts
}

function mount() {
  const added = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <BringDomainPanel onAdded={added} />
    </ClientProvider>
  )
  return added
}

async function typeName(name: string) {
  await userEvent.type(
    screen.getByRole("textbox", { name: "Domain you own" }),
    name
  )
}

it("shows the two ways to bring a domain, as in the mock", async () => {
  stub([connection("cf", "cloudflare")])
  mount()
  expect(screen.getByRole("region", { name: "Bring a domain" })).toBeTruthy()
  expect(
    screen
      .getByRole("textbox", { name: "Domain you own" })
      .getAttribute("placeholder")
  ).toBe("example.com")
  expect(screen.getByText("Loomplane manages its DNS")).toBeTruthy()
  expect(
    screen.getByText(
      "Pick the connected account that hosts the zone. Loomplane only changes records it created."
    )
  ).toBeTruthy()
  expect(screen.getByText("Keep DNS where it is")).toBeTruthy()
  expect(
    screen.getByText(
      "Prove you own it with a TXT record. You add each hostname's record yourself, and Loomplane checks it."
    )
  ).toBeTruthy()
  for (const name of [
    "Cloudflare",
    "DigitalOcean",
    "Vercel",
    "Verify with TXT",
  ])
    expect(screen.getByRole("button", { name })).toBeTruthy()
})

it("verifies with TXT for DNS kept elsewhere", async () => {
  const posts = stub([connection("cf", "cloudflare")])
  const added = mount()
  await typeName("docs-example.org")
  await userEvent.click(screen.getByRole("button", { name: "Verify with TXT" }))
  await waitFor(() => expect(added).toHaveBeenCalled())
  expect(posts).toEqual([
    { name: "docs-example.org", mode: "external", project_ids: [] },
  ])
  expect(added.mock.calls[0][0]).toMatchObject({ id: "zone" })
})

it("lists the provider's ready connections when there are several", async () => {
  const posts = stub([
    connection("a", "digitalocean"),
    connection("b", "digitalocean"),
    connection("c", "digitalocean", "failed"),
    connection("cf", "cloudflare"),
  ])
  const added = mount()
  await typeName("acme.dev")
  await userEvent.click(
    await screen.findByRole("button", { name: "DigitalOcean" })
  )
  expect(posts).toEqual([])
  expect(screen.queryByRole("button", { name: /digitalocean c/ })).toBeNull()
  await userEvent.click(screen.getByRole("button", { name: /digitalocean b/ }))
  await waitFor(() => expect(added).toHaveBeenCalled())
  expect(posts).toEqual([
    { name: "acme.dev", mode: "managed", connection_id: "b", project_ids: [] },
  ])
})

it("opens the connect dialog when the provider has no account yet", async () => {
  const posts = stub([connection("cf", "cloudflare")])
  mount()
  await typeName("acme.dev")
  await userEvent.click(await screen.findByRole("button", { name: "Vercel" }))
  expect(
    await screen.findByRole("dialog", { name: "Connect an account" })
  ).toBeTruthy()
  expect(posts).toEqual([])
})

it("says when the account doesn't host the zone", async () => {
  stub([connection("cf", "cloudflare")], () =>
    Response.json({ message: "not found" }, { status: 404 })
  )
  const added = mount()
  await typeName("acme.dev")
  await userEvent.click(
    await screen.findByRole("button", { name: "Cloudflare" })
  )
  expect(
    await screen.findByText("acme.dev isn't a zone in that Cloudflare account.")
  ).toBeTruthy()
  expect(added).not.toHaveBeenCalled()
})

it("asks for the domain before adding anything", async () => {
  const posts = stub([connection("cf", "cloudflare")])
  mount()
  await userEvent.click(screen.getByRole("button", { name: "Verify with TXT" }))
  expect(await screen.findByText("Enter the domain you own.")).toBeTruthy()
  expect(posts).toEqual([])
})
