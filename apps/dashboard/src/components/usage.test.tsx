import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import {
  AccountSpend,
  LatencyCard,
  SpendAccounts,
  TrafficCard,
  UsageSummary,
} from "@/components/usage"

vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
  }: {
    children: React.ReactNode
    to: string
    params?: Record<string, string>
  }) => (
    <a href={to.replace(/\$(\w+)/g, (_, k: string) => params?.[k] ?? "")}>
      {children}
    </a>
  ),
}))

const day = (d: number) => `2026-10-${String(d).padStart(2, "0")}T00:00:00Z`

// The API speaks snake_case; the client's codecs turn it into camelCase.
const fullSpend = () => ({
  month: "2026-10-01T00:00:00Z",
  totals: [
    { currency: "EUR", total: 7, days: [{ t: day(1), v: 7 }], undated: 0 },
    {
      currency: "USD",
      total: 35,
      undated: 0,
      days: [
        { t: day(1), v: 10 },
        { t: day(2), v: 25 },
      ],
    },
  ],
  accounts: [
    {
      connection_id: "aws",
      cloud: "aws",
      name: "Production",
      currency: "USD",
      total: 30,
      days: [],
    },
    {
      connection_id: "eu",
      cloud: "gcp",
      name: "Europe",
      currency: "EUR",
      total: 7,
      days: [],
    },
  ],
  sources: [
    {
      connection_id: "aws",
      cloud: "aws",
      name: "Production",
      status: "reporting",
    },
    { connection_id: "eu", cloud: "gcp", name: "Europe", status: "reporting" },
    {
      connection_id: "gcp",
      cloud: "gcp",
      name: "Shop",
      status: "unavailable",
      reason: "Add the billing export's table to this connection.",
    },
    {
      connection_id: "site",
      cloud: "vercel",
      name: "Site",
      status: "failed",
      reason: "The cloud didn't answer.",
    },
  ],
})

let spend = fullSpend()

const traffic = (range: string, total: number) => ({
  range,
  start: day(17),
  end: day(24),
  step: 10800,
  requests: [
    { t: day(17), v: total - 10 },
    { t: day(18), v: 10 },
  ],
  total,
  previous_total: 100,
  services: [
    {
      binding_id: "api",
      project_id: "p1",
      environment_id: "e1",
      name: "api",
      provider: "cloud-run",
      requests: 900,
      previous_requests: 800,
      latency: 240.4,
      previous_latency: 300,
      statistic: "p95",
    },
    {
      binding_id: "worker",
      project_id: "p1",
      environment_id: "e1",
      name: "worker",
      provider: "azure-container-apps",
      requests: 30,
      previous_requests: 0,
      latency: 120,
      previous_latency: 0,
      statistic: "average",
    },
  ],
  sources: [
    {
      connection_id: "gcp",
      cloud: "gcp",
      name: "Shop",
      status: "reporting",
      services: ["api"],
    },
    {
      connection_id: "site",
      cloud: "vercel",
      name: "Site",
      status: "unsupported",
      reason:
        "Vercel doesn't report requests or latency through an API Loomplane reads.",
      services: ["web"],
    },
  ],
})

let asked: string[] = []
let trafficOverride: Record<string, unknown> = {}

beforeEach(() => {
  asked = []
  spend = fullSpend()
  trafficOverride = {}
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    asked.push(url.pathname + url.search)
    let body: unknown = {}
    if (url.pathname === "/api/v1/usage/spend") body = spend
    if (url.pathname === "/api/v1/usage/traffic") {
      const range = url.searchParams.get("range") ?? "7d"
      body = {
        ...traffic(range, range === "24h" ? 50 : 1200),
        ...trafficOverride,
      }
    }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const wrap = (ui: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)

it("lists each account's spend in its own currency, and says why one is missing", async () => {
  wrap(<SpendAccounts />)
  expect(await screen.findByText("$30.00")).toBeInTheDocument()
  expect(screen.getByText("€7.00")).toBeInTheDocument()
  expect(
    screen.getByText("Add the billing export's table to this connection.")
  ).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Shop" })).toHaveAttribute(
    "href",
    "/clouds/accounts/gcp"
  )
  expect(screen.getByText("The cloud didn't answer.")).toBeInTheDocument()
})

it("ranks services by latency and never calls an average a p95", async () => {
  wrap(<LatencyCard />)
  const card = await screen.findByRole("region", { name: "Slowest services" })
  const rows = await within(card).findAllByRole("listitem")
  expect(rows[0]).toHaveTextContent("api")
  expect(rows[0]).toHaveTextContent("240 ms")
  expect(rows[0]).toHaveTextContent("p95")
  expect(rows[0]).toHaveTextContent("-20%")
  expect(rows[1]).toHaveTextContent("worker")
  expect(rows[1]).toHaveTextContent("avg")
  expect(rows[1]).not.toHaveTextContent("p95")
  expect(
    within(card).getByText(
      "Vercel doesn't report latency through an API Loomplane reads."
    )
  ).toBeInTheDocument()
})

it("totals traffic for the chosen range against the one before", async () => {
  wrap(<TrafficCard />)
  const card = await screen.findByRole("region", { name: "Traffic" })
  expect(await within(card).findByText("1.2K")).toBeInTheDocument()
  expect(within(card).getByText(/\+1100%/)).toBeInTheDocument()
  await userEvent.click(within(card).getByRole("button", { name: "24 hours" }))
  expect(await within(card).findByText("50")).toBeInTheDocument()
  expect(asked).toContain("/api/v1/usage/traffic?range=24h")
})

it("shows one account's spend, or why it has none", async () => {
  const { unmount } = wrap(<AccountSpend connectionId="aws" />)
  expect(await screen.findByText("$30.00")).toBeInTheDocument()
  unmount()
  forgeClient.clear()
  wrap(<AccountSpend connectionId="gcp" />)
  expect(
    await screen.findByText(
      "Add the billing export's table to this connection."
    )
  ).toBeInTheDocument()
})

it("breaks spend down by account, largest first", async () => {
  wrap(<SpendAccounts />)
  const rows = await screen.findAllByRole("row")
  expect(rows[1]).toHaveTextContent("Production")
  expect(rows[1]).toHaveTextContent("$30.00")
  expect(
    within(rows[1]!).getByRole("link", { name: "Production" })
  ).toHaveAttribute("href", "/clouds/accounts/aws")
})

const source = (
  connection_id: string,
  cloud: string,
  status: string,
  reason = ""
) => ({ connection_id, cloud, name: connection_id, status, reason })

it("says a shared reason once and folds clouds with no API into one line", async () => {
  const role = "Make the token as an owner."
  spend = {
    month: spend.month,
    totals: [],
    accounts: [],
    sources: [
      source("Digital Ocean", "digitalocean", "unavailable", role),
      source("Billing", "digitalocean", "unavailable", role),
      source("Rendering", "render", "unsupported", "Render has no API."),
      source("Personal", "vercel", "unsupported", "Vercel has no API."),
      source("Test", "vercel", "unsupported", "Vercel has no API."),
    ],
  }
  wrap(<SpendAccounts />)
  const card = await screen.findByRole("list", { name: "Not reporting" })
  const notes = within(card).getAllByRole("listitem")
  expect(notes).toHaveLength(2)
  expect(
    within(notes[0]!).getByRole("link", { name: "Digital Ocean" })
  ).toBeInTheDocument()
  expect(
    within(notes[0]!).getByRole("link", { name: "Billing" })
  ).toBeInTheDocument()
  expect(within(card).getAllByText(role)).toHaveLength(1)
  expect(notes[1]).toHaveTextContent(
    "Render and Vercel don't report spend through an API Loomplane reads."
  )
})

it("shows two things to fix on a card and the rest when asked", async () => {
  trafficOverride = {
    sources: ["a", "b", "c", "d"].map((id) => ({
      ...source(id, "gcp", "unavailable", `Fix ${id}.`),
      services: [id],
    })),
  }
  wrap(<LatencyCard />)
  const card = await screen.findByRole("region", { name: "Slowest services" })
  expect(await within(card).findByText("Fix a.")).toBeInTheDocument()
  expect(within(card).getByText("Fix b.")).toBeInTheDocument()
  expect(within(card).queryByText("Fix c.")).not.toBeInTheDocument()
  await userEvent.click(within(card).getByRole("button", { name: "2 more" }))
  expect(within(card).getByText("Fix c.")).toBeInTheDocument()
})

const row = (card: HTMLElement, label: string) =>
  within(card).getByText(label).closest("div")!

it("sums up spend, latency and traffic in one card, the way it always looked", async () => {
  wrap(<UsageSummary />)
  const card = await screen.findByRole("region", {
    name: "Spend, latency and traffic",
  })
  await within(card).findByText("$35.00")
  expect(row(card, "Cloud spend")).toHaveTextContent("$35.00")
  expect(row(card, "p95 latency")).toHaveTextContent("240 ms")
  expect(row(card, "Traffic")).toHaveTextContent("1.2K this week")
  // Two connections can't report: point at the details, don't list them.
  expect(
    within(card).getByRole("link", { name: "See what's missing" })
  ).toHaveAttribute("href", "/infrastructure")
  expect(within(card).queryByText(/billing export/)).not.toBeInTheDocument()
})

it("says not connected and asks for a cloud when nothing is", async () => {
  spend = { month: spend.month, totals: [], accounts: [], sources: [] }
  trafficOverride = { services: [], sources: [], total: 0 }
  wrap(<UsageSummary />)
  const card = await screen.findByRole("region", {
    name: "Spend, latency and traffic",
  })
  await within(card).findAllByText("Not connected")
  expect(within(card).getAllByText("Not connected")).toHaveLength(3)
  expect(
    within(card).getByRole("link", { name: "Connect a cloud" })
  ).toHaveAttribute("href", "/integrations")
})

it("tells a fix apart from a cloud that can't report at all", async () => {
  spend = {
    month: spend.month,
    totals: [],
    accounts: [],
    sources: [
      source("do", "digitalocean", "unavailable", "Make it as an owner."),
    ],
  }
  trafficOverride = {
    services: [],
    total: 0,
    sources: [
      {
        ...source("site", "vercel", "unsupported", "No API."),
        services: ["web"],
      },
    ],
  }
  wrap(<UsageSummary />)
  const card = await screen.findByRole("region", {
    name: "Spend, latency and traffic",
  })
  await within(card).findByText("Needs a fix")
  expect(row(card, "Cloud spend")).toHaveTextContent("Needs a fix")
  expect(row(card, "Traffic")).toHaveTextContent("Not reported")
  expect(row(card, "p95 latency")).toHaveTextContent("Not reported")
})
