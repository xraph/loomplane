import { ClientProvider } from "@forge-go/client-react"
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { RegisterDomainDialog } from "./register-domain-dialog"

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const zero = "0001-01-01T00:00:00Z"
const usd = (cost: string) => ({
  currency: "USD",
  registration_cost: cost,
  renewal_cost: cost,
})

type Offer = {
  name: string
  registrable: boolean
  reason?: string
  price?: ReturnType<typeof usd>
}

const offers: Offer[] = [
  { name: "acmeshop.app", registrable: true, price: usd("10.11") },
  { name: "acmeshop.dev", registrable: false, reason: "domain_unavailable" },
  { name: "acmeshop.shop", registrable: false, reason: "domain_unavailable" },
  {
    name: "acmeshop.ai",
    registrable: false,
    reason: "extension_not_supported_via_api",
  },
]

function cloudflare(
  id: string,
  details: Record<string, string> = { registrar: "granted" },
  status = "ready"
) {
  return {
    id,
    cloud: "cloudflare",
    name: `Cloudflare ${id}`,
    account: "Acme Inc",
    status,
    details: { account_id: `acct-${id}`, ...details },
    missing: [],
  }
}

type Calls = { method: string; path: string; body?: unknown }[]

function stubRegistration(
  options: {
    connections?: unknown[] | (() => unknown[])
    offers?: Offer[]
    extensions?: string[]
    check?: (name: string) => Offer
    quote?: ReturnType<typeof usd> | ((n: number) => ReturnType<typeof usd>)
    reviewExpiresAt?: string
    approve?: (n: number) => Response
    sandbox?: (n: number) => boolean
  } = {}
): Calls {
  const calls: Calls = []
  let previews = 0
  let approvals = 0
  const all = options.offers ?? offers
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const path = url.pathname
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, path: path + url.search, body })
      if (path === "/api/v1/connections")
        return Response.json({
          connections:
            typeof options.connections === "function"
              ? options.connections()
              : (options.connections ?? [cloudflare("cf")]),
        })
      if (path === "/api/v1/connection-clouds")
        return Response.json({
          clouds: [
            {
              cloud: "cloudflare",
              title: "Cloudflare",
              builtin: false,
              credential: "API token",
              methods: ["token"],
              settings: [{ key: "account_id", label: "Account ID" }],
            },
          ],
        })
      if (path === "/api/v1/domains")
        return Response.json({
          zones: [zoneNamed("acmeshop.shop")],
          registrations: [],
        })
      if (path === "/api/v1/domain-registrations" && method === "GET")
        return Response.json({ registrations: [] })
      if (path === "/api/v1/domain-registration/extensions")
        return Response.json({
          extensions: options.extensions ?? ["com", "app", "dev", "shop"],
        })
      if (path === "/api/v1/domain-registration/search")
        return Response.json({ offers: all })
      if (path === "/api/v1/domain-registration/check") {
        const name = (body as { domains: string[] }).domains[0]
        const offer = options.check?.(name) ?? all.find((o) => o.name === name)
        return Response.json({ offers: [offer] })
      }
      if (path === "/api/v1/domain-registrations/preview") {
        previews++
        const quote =
          typeof options.quote === "function"
            ? options.quote(previews)
            : (options.quote ?? usd("10.11"))
        return Response.json(registration(body, quote, previews, options))
      }
      if (path.endsWith("/approve")) {
        approvals++
        if (options.approve) return options.approve(approvals)
        return Response.json(
          {
            ...registration({ name: "acmeshop.app" }, usd("10.11"), 1, options),
            status: "approved",
          },
          { status: 202 }
        )
      }
      return Response.json({}, { status: 404 })
    }
  )
  return calls
}

function registration(
  body: unknown,
  quote: ReturnType<typeof usd>,
  n: number,
  options: { reviewExpiresAt?: string; sandbox?: (n: number) => boolean }
) {
  const b = body as {
    name: string
    connection_id?: string
    auto_renew?: boolean
    privacy_mode?: string
  }
  return {
    id: `reg-${n}`,
    org_id: "org",
    connection_id: b.connection_id ?? "cf",
    provider: "cloudflare",
    account_id: "acct-cf",
    name: b.name,
    auto_renew: b.auto_renew ?? true,
    privacy_mode: b.privacy_mode ?? "redaction",
    custom_registrant: false,
    project_ids: [],
    quote,
    status: "preview",
    message: "",
    review: n === 1 ? "r" : `r${n}`,
    review_expires_at:
      options.reviewExpiresAt ??
      new Date(Date.now() + 15 * 60 * 1000).toISOString(),
    attempt: 0,
    attempted: false,
    first_unconfirmed_at: zero,
    zone_id: "",
    domain_expires_at: zero,
    refreshed_at: zero,
    notice_sent_for: zero,
    result_notified_for: "",
    sandbox: options.sandbox?.(n) ?? false,
    revision: 1,
    created_at: zero,
    updated_at: zero,
  }
}

function zoneNamed(name: string) {
  return {
    zone: {
      id: `zone-${name}`,
      org_id: "org",
      name,
      mode: "managed",
      provider: "cloudflare",
      connection_id: "cf",
      project_ids: [],
      proof_checked_at: zero,
      proof_expires_at: zero,
      provider_zone: {},
      record_ownership: true,
      registration_id: "",
      revision: 1,
      verified_at: "2026-10-01T00:00:00Z",
    },
    state: "ready",
    wildcards: false,
    registration_id: "",
    hostnames: [],
  }
}

function Wrapped({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

function mount(onOpenChange: (open: boolean) => void = () => {}) {
  render(
    <Wrapped>
      <RegisterDomainDialog open onOpenChange={onOpenChange} />
    </Wrapped>
  )
}

async function search(q = "acmeshop") {
  await userEvent.type(
    await screen.findByRole("searchbox", { name: "Find a name" }),
    q
  )
  return screen.findByRole("list", { name: "Names" })
}

async function searchAndSelect(name: string) {
  const list = await search()
  const row = await within(list).findByRole("listitem", { name })
  await userEvent.click(within(row).getByRole("button", { name: "Select" }))
}

const posts = (calls: Calls, suffix: string) =>
  calls.filter((c) => c.method === "POST" && c.path.endsWith(suffix))

function lastBody(calls: Calls, suffix: string) {
  return posts(calls, suffix).at(-1)?.body
}

const consent = /I understand Cloudflare charges USD 10\.11/

it("shows the header and the account like the mock", async () => {
  stubRegistration()
  mount()
  expect(
    await screen.findByRole("dialog", { name: "Register a domain" })
  ).toBeTruthy()
  expect(
    screen.getByText(
      "Bought with your Cloudflare account and billed to it. The domain is yours, and Loomplane manages its DNS."
    )
  ).toBeTruthy()
  const account = await screen.findByRole("combobox", {
    name: "Cloudflare account",
  })
  expect(account.textContent).toBe("Acme Inc · acct-cf")
  expect(
    screen.getByText(
      "Only extensions Cloudflare's API can register are offered. Prices come from Cloudflare and are shown before you buy."
    )
  ).toBeTruthy()
  expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy()
  expect(screen.queryByText("Sandbox")).toBeNull()
})

it("labels search results like the mock", async () => {
  const calls = stubRegistration()
  mount()
  const list = await search()
  const row = (name: string) => within(list).findByRole("listitem", { name })
  const app = await row("acmeshop.app")
  expect(within(app).getByText("Available")).toBeTruthy()
  expect(within(app).getByText("USD 10.11 / year")).toBeTruthy()
  expect(within(await row("acmeshop.dev")).getByText("Taken")).toBeTruthy()
  expect(
    within(await row("acmeshop.shop")).getByText("You own this")
  ).toBeTruthy()
  expect(
    within(await row("acmeshop.ai")).getByText(
      "Not offered through the API yet"
    )
  ).toBeTruthy()
  expect(within(list).getAllByRole("button", { name: "Select" })).toHaveLength(
    1
  )
  expect(within(app).getByRole("button", { name: "Select" })).toBeTruthy()
  expect(
    calls.some(
      (c) =>
        c.path ===
        "/api/v1/domain-registration/search?connection_id=cf&q=acmeshop"
    )
  ).toBe(true)
})

it("takes the extension list from the API", async () => {
  stubRegistration({
    extensions: ["com", "app"],
    offers: [
      { name: "acmeshop.app", registrable: true, price: usd("10.11") },
      { name: "acmeshop.io", registrable: true, price: usd("40.00") },
    ],
  })
  mount()
  const list = await search()
  const io = await within(list).findByRole("listitem", { name: "acmeshop.io" })
  expect(within(io).getByText("Not offered through the API yet")).toBeTruthy()
  expect(within(io).queryByRole("button", { name: "Select" })).toBeNull()
})

it("never buys without consent and the previewed price", async () => {
  const calls = stubRegistration({ quote: usd("10.11") })
  mount()
  await searchAndSelect("acmeshop.app")
  const buy = await screen.findByRole("button", {
    name: "Buy acmeshop.app for USD 10.11",
  })
  expect(buy).toHaveProperty("disabled", true)
  await userEvent.click(buy)
  expect(posts(calls, "/approve")).toHaveLength(0)

  // The facts table, as in the mock.
  for (const text of [
    "acmeshop.app",
    "USD 10.11 for the first year, charged now",
    "Cloudflare · Acme Inc · acct-cf",
    "The default contact on this Cloudflare account",
    "Loomplane creates the DNS zone, so you can attach hostnames to any project.",
  ])
    expect(screen.getAllByText(text).length).toBeGreaterThan(0)
  expect(lastBody(calls, "/check")).toEqual({
    connection_id: "cf",
    domains: ["acmeshop.app"],
  })

  await userEvent.click(screen.getByRole("checkbox", { name: consent }))
  expect(buy).toHaveProperty("disabled", false)
  await userEvent.dblClick(buy)
  await screen.findByRole("status")
  expect(posts(calls, "/approve")).toHaveLength(1)
  expect(posts(calls, "/approve")[0].path).toBe(
    "/api/v1/domain-registrations/reg-1/approve"
  )
  expect(lastBody(calls, "/approve")).toEqual({
    review: "r",
    currency: "USD",
    registration_cost: "10.11",
  })
})

it("starts with auto-renew and WHOIS privacy on", async () => {
  const calls = stubRegistration()
  mount()
  await searchAndSelect("acmeshop.app")
  await screen.findByRole("button", { name: "Buy acmeshop.app for USD 10.11" })
  expect(lastBody(calls, "/preview")).toEqual({
    connection_id: "cf",
    name: "acmeshop.app",
    auto_renew: true,
    privacy_mode: "redaction",
  })
  const renew = screen.getByRole("checkbox", {
    name: /Renew automatically every year\./,
  })
  const privacy = screen.getByRole("checkbox", {
    name: /Hide my contact details in WHOIS/,
  })
  expect(renew).toHaveProperty("checked", true)
  expect(privacy).toHaveProperty("checked", true)
  expect(
    screen.getByText(
      "Loomplane reminds you 30 days before each renewal, with the date and the price."
    )
  ).toBeTruthy()
  expect(screen.getByText("where the extension allows it.")).toBeTruthy()

  await userEvent.click(privacy)
  await waitFor(() => expect(posts(calls, "/preview")).toHaveLength(2))
  expect(lastBody(calls, "/preview")).toMatchObject({
    auto_renew: true,
    privacy_mode: "off",
  })
  await userEvent.click(renew)
  await waitFor(() => expect(posts(calls, "/preview")).toHaveLength(3))
  expect(lastBody(calls, "/preview")).toMatchObject({
    auto_renew: false,
    privacy_mode: "off",
  })

  // The approval carries the last preview's review, which holds both.
  const consentBox = await screen.findByRole("checkbox", { name: consent })
  await userEvent.click(consentBox)
  await userEvent.click(
    screen.getByRole("button", { name: "Buy acmeshop.app for USD 10.11" })
  )
  await waitFor(() => expect(posts(calls, "/approve")).toHaveLength(1))
  expect(posts(calls, "/approve")[0].path).toBe(
    "/api/v1/domain-registrations/reg-3/approve"
  )
  expect(lastBody(calls, "/approve")).toEqual({
    review: "r3",
    currency: "USD",
    registration_cost: "10.11",
  })
})

it("unticks consent when a fresh preview changes the price", async () => {
  stubRegistration({ quote: (n) => usd(n === 1 ? "10.11" : "12.00") })
  mount()
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  await userEvent.click(
    screen.getByRole("checkbox", { name: /Hide my contact details/ })
  )
  const buy = await screen.findByRole("button", {
    name: "Buy acmeshop.app for USD 12.00",
  })
  expect(
    screen.getByRole("checkbox", {
      name: /I understand Cloudflare charges USD 12\.00/,
    })
  ).toHaveProperty("checked", false)
  expect(buy).toHaveProperty("disabled", true)
})

it("keeps consent when a fresh preview keeps the price", async () => {
  const calls = stubRegistration()
  mount()
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  await userEvent.click(
    screen.getByRole("checkbox", { name: /Hide my contact details/ })
  )
  await waitFor(() => expect(posts(calls, "/preview")).toHaveLength(2))
  const buy = screen.getByRole("button", {
    name: "Buy acmeshop.app for USD 10.11",
  })
  await waitFor(() => expect(buy).toHaveProperty("disabled", false))
  expect(screen.getByRole("checkbox", { name: consent })).toHaveProperty(
    "checked",
    true
  )
})

it("disables Buy once the review window lapses", async () => {
  vi.useFakeTimers({
    shouldAdvanceTime: true,
    toFake: ["setInterval", "clearInterval", "Date"],
  })
  const expires = new Date(Date.now() + 60 * 1000).toISOString()
  stubRegistration({ reviewExpiresAt: expires })
  mount()
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  const buy = screen.getByRole("button", {
    name: "Buy acmeshop.app for USD 10.11",
  })
  expect(buy).toHaveProperty("disabled", false)
  await act(async () => {
    vi.advanceTimersByTime(61 * 1000)
  })
  expect(buy).toHaveProperty("disabled", true)
})

it("goes back to Search when the check finds the name taken", async () => {
  const calls = stubRegistration({
    check: (name) => ({
      name,
      registrable: false,
      reason: "domain_unavailable",
    }),
  })
  mount()
  await searchAndSelect("acmeshop.app")
  const list = screen.getByRole("list", { name: "Names" })
  const app = within(list).getByRole("listitem", { name: "acmeshop.app" })
  await within(app).findByText("Taken")
  expect(within(app).queryByRole("button", { name: "Select" })).toBeNull()
  expect(posts(calls, "/preview")).toHaveLength(0)
})

it("asks for a fresh review when the quote moved", async () => {
  const calls = stubRegistration({
    quote: (n) => usd(n === 1 ? "10.11" : "12.00"),
    approve: () =>
      Response.json(
        { message: "the approved price isn't the quoted one" },
        { status: 409 }
      ),
  })
  mount()
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  await userEvent.click(
    screen.getByRole("button", { name: "Buy acmeshop.app for USD 10.11" })
  )
  const buy = await screen.findByRole("button", {
    name: "Buy acmeshop.app for USD 12.00",
  })
  expect(buy).toHaveProperty("disabled", true)
  expect(
    screen.getByText(
      "The price or the review changed before purchase. Nothing was charged. Check the new price and approve it again."
    )
  ).toBeTruthy()
  expect(posts(calls, "/approve")).toHaveLength(1)
  expect(posts(calls, "/preview")).toHaveLength(2)
})

it("shows the pending state after approval and goes back to Domains", async () => {
  stubRegistration()
  const onOpenChange = vi.fn()
  mount(onOpenChange)
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  await userEvent.click(
    screen.getByRole("button", { name: "Buy acmeshop.app for USD 10.11" })
  )
  const status = await screen.findByRole("status")
  expect(status.textContent).toBe(
    "Cloudflare is registering acmeshop.app. You can close this. Loomplane keeps checking and notifies you when it's done, then sets up its DNS zone."
  )
  expect(
    screen.getByText(
      "If Cloudflare declines the payment, nothing is registered and you'll see why on the Domains page."
    )
  ).toBeTruthy()
  await userEvent.click(screen.getByRole("button", { name: "Go to Domains" }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
})

it("keeps the quoted account when the connection list changes", async () => {
  let aGranted = true
  const calls = stubRegistration({
    connections: () => [
      {
        ...cloudflare("a", aGranted ? { registrar: "granted" } : {}),
        account: "Acme A",
      },
      { ...cloudflare("b"), account: "Acme B" },
    ],
  })
  mount()
  await userEvent.selectOptions(
    await screen.findByRole("combobox", { name: "Cloudflare account" }),
    "a"
  )
  await searchAndSelect("acmeshop.app")
  await userEvent.click(await screen.findByRole("checkbox", { name: consent }))
  const buy = screen.getByRole("button", {
    name: "Buy acmeshop.app for USD 10.11",
  })
  expect(buy).toHaveProperty("disabled", false)

  // Account A loses its Registrar permission while the review is open.
  aGranted = false
  await act(async () => {
    forgeClient.invalidate(["Connection[]"])
  })
  await screen.findByText(
    "This Cloudflare account can't register domains any more, so this price can't be approved."
  )
  expect(screen.getByText("Cloudflare · Acme A · acct-a")).toBeTruthy()
  expect(screen.queryByText(/Acme B/)).toBeNull()
  expect(buy).toHaveProperty("disabled", true)
  const privacy = screen.getByRole("checkbox", {
    name: /Hide my contact details in WHOIS/,
  })
  expect(privacy).toHaveProperty("disabled", true)
  await userEvent.click(privacy)
  await userEvent.click(buy)
  const previews = posts(calls, "/preview")
  expect(previews).toHaveLength(1)
  expect(previews[0].body).toMatchObject({ connection_id: "a" })
  expect(posts(calls, "/approve")).toHaveLength(0)
  expect(posts(calls, "/check").map((c) => c.body)).toEqual([
    { connection_id: "a", domains: ["acmeshop.app"] },
  ])
})

it("marks a sandbox account", async () => {
  stubRegistration({
    connections: [
      cloudflare("cf", { registrar: "granted", registrar_mode: "sandbox" }),
    ],
  })
  mount()
  expect(await screen.findByText("Sandbox")).toBeTruthy()
  expect(
    screen.getByText(
      "Sandbox registrations are test records. Nothing is charged."
    )
  ).toBeTruthy()
})

it("names the charge the preview reports, not the connection's mode", async () => {
  // The connection was checked in the sandbox; the registrar now isn't.
  stubRegistration({
    connections: [
      cloudflare("cf", { registrar: "granted", registrar_mode: "sandbox" }),
    ],
    sandbox: () => false,
  })
  mount()
  await searchAndSelect("acmeshop.app")
  expect(await screen.findByRole("checkbox", { name: consent })).toBeTruthy()
  expect(screen.queryByText("Sandbox")).toBeNull()
  expect(screen.queryByText(/Nothing is charged/)).toBeNull()
})

it("follows each preview's sandbox answer in the consent line", async () => {
  const calls = stubRegistration({ sandbox: (n) => n === 1 })
  mount()
  await searchAndSelect("acmeshop.app")
  const sandboxConsent = await screen.findByRole("checkbox", {
    name: "I understand this is a sandbox registration at USD 10.11. Nothing is charged.",
  })
  expect(screen.getByText("Sandbox")).toBeTruthy()
  expect(
    screen.getByText("USD 10.11 for the first year, not charged in the sandbox")
  ).toBeTruthy()
  expect(screen.queryByRole("checkbox", { name: consent })).toBeNull()
  expect(
    screen.getByText("Sandbox registrations don't create a DNS zone.")
  ).toBeTruthy()
  expect(
    screen.queryByText(
      "Loomplane creates the DNS zone, so you can attach hostnames to any project."
    )
  ).toBeNull()
  await userEvent.click(sandboxConsent)

  // The next preview answers from the production registrar.
  await userEvent.click(
    screen.getByRole("checkbox", { name: /Hide my contact details in WHOIS/ })
  )
  await waitFor(() => expect(posts(calls, "/preview")).toHaveLength(2))
  const charged = await screen.findByRole("checkbox", { name: consent })
  expect(charged).toHaveProperty("checked", false)
  expect(
    screen.getByText("USD 10.11 for the first year, charged now")
  ).toBeTruthy()
  expect(
    screen.getByText(
      "Loomplane creates the DNS zone, so you can attach hostnames to any project."
    )
  ).toBeTruthy()
  expect(
    screen.queryByText("Sandbox registrations don't create a DNS zone.")
  ).toBeNull()
  expect(screen.queryByText("Sandbox")).toBeNull()
  expect(screen.queryByText(/Nothing is charged/)).toBeNull()
})

it("asks for a Cloudflare connection with Registrar permission", async () => {
  const calls = stubRegistration({
    connections: [
      cloudflare("cf", {}),
      cloudflare("off", { registrar: "granted" }, "failed"),
    ],
  })
  mount()
  expect(
    await screen.findByText(
      "Registration needs a Cloudflare connection with Registrar edit permission."
    )
  ).toBeTruthy()
  expect(screen.queryByRole("searchbox", { name: "Find a name" })).toBeNull()
  await userEvent.click(
    screen.getByRole("button", { name: "Connect Cloudflare" })
  )
  expect(
    await screen.findByRole("dialog", { name: "Connect an account" })
  ).toBeTruthy()
  expect(
    calls.some((c) => c.path.startsWith("/api/v1/domain-registration/"))
  ).toBe(false)
})
