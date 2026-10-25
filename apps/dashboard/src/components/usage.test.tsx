import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import {
  AccountSpend,
  LatencyCard,
  SpendAccounts,
  SpendCard,
  TrafficCard,
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

beforeEach(() => {
  asked = []
  spend = fullSpend()
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
      body = traffic(range, range === "24h" ? 50 : 1200)
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

it("leads spend with the largest currency, keeps the rest apart, and says why an account is missing", async () => {
  wrap(<SpendCard />)
  const card = await screen.findByRole("region", { name: "Cloud spend" })
  expect(await within(card).findByText("$35.00")).toBeInTheDocument()
  expect(within(card).getByText(/€7\.00/)).toBeInTheDocument()
  expect(
    within(card).getByText("Add the billing export's table to this connection.")
  ).toBeInTheDocument()
  expect(within(card).getByRole("link", { name: "Shop" })).toHaveAttribute(
    "href",
    "/clouds/accounts/gcp"
  )
  expect(within(card).getByText("The cloud didn't answer.")).toBeInTheDocument()
})

it("asks for a cloud when nothing is connected", async () => {
  spend = { month: spend.month, totals: [], accounts: [], sources: [] }
  wrap(<SpendCard />)
  const card = await screen.findByRole("region", { name: "Cloud spend" })
  expect(await within(card).findByText("None yet")).toBeInTheDocument()
  expect(
    within(card).getByRole("link", { name: "Connect a cloud" })
  ).toHaveAttribute("href", "/integrations")
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

it("says how much of the month has no daily breakdown", async () => {
  spend = {
    month: spend.month,
    totals: [
      {
        currency: "USD",
        total: 184.62,
        days: [{ t: day(1), v: 0 }],
        undated: 184.62,
      },
    ],
    accounts: [
      {
        connection_id: "do",
        cloud: "digitalocean",
        name: "Team",
        currency: "USD",
        total: 184.62,
        days: [],
      },
    ],
    sources: [
      {
        connection_id: "do",
        cloud: "digitalocean",
        name: "Team",
        status: "reporting",
      },
    ],
  }
  wrap(<SpendCard />)
  const card = await screen.findByRole("region", { name: "Cloud spend" })
  expect(await within(card).findByText("$185")).toBeInTheDocument()
  expect(
    within(card).getByText("$185 isn't broken down by day")
  ).toBeInTheDocument()
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
  wrap(<SpendCard />)
  const card = await screen.findByRole("region", { name: "Cloud spend" })
  const notes = await within(card).findAllByRole("listitem")
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

it("shows two things to fix on the overview and points at the rest", async () => {
  spend = {
    month: spend.month,
    totals: [],
    accounts: [],
    sources: ["a", "b", "c", "d"].map((id) =>
      source(id, "gcp", "unavailable", `Fix ${id}.`)
    ),
  }
  wrap(<SpendCard />)
  const card = await screen.findByRole("region", { name: "Cloud spend" })
  expect(await within(card).findByText("Fix a.")).toBeInTheDocument()
  expect(within(card).getByText("Fix b.")).toBeInTheDocument()
  expect(within(card).queryByText("Fix c.")).not.toBeInTheDocument()
  expect(within(card).getByRole("link", { name: "2 more" })).toHaveAttribute(
    "href",
    "/infrastructure"
  )
})
