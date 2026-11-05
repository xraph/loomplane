import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import type { Account } from "./client"
import { BillingPage } from "./billing-page"

const state = vi.hoisted(() => ({
  account: null as unknown,
  invoices: [] as unknown[],
  owner: true,
  limitSaves: [] as (string | null)[],
  limitAnswer: { accepted: false },
  ai: [] as boolean[],
  assigned: [] as string[],
}))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) => p !== "organization:administer" || state.owner,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("./client", () => ({
  invalidateBilling: () => {},
  useBillingAccount: () => ({
    data: state.account,
    error: undefined,
    loading: false,
    refetch: () => {},
  }),
  useInvoices: () => ({
    data: state.invoices,
    error: undefined,
    loading: false,
    refetch: () => {},
  }),
  useInvoiceLink: () => ({
    mutateAsync: async () => ({ url: "https://stripe.test/invoice" }),
    isPending: false,
  }),
  useSetupSession: () => ({
    mutateAsync: async () => ({ url: "https://stripe.test/setup" }),
    isPending: false,
  }),
  useSetSpendingLimit: () => ({
    mutateAsync: async (limit: string | null) => {
      state.limitSaves.push(limit)
      return state.limitAnswer
    },
    isPending: false,
  }),
  useSetAI: () => ({
    mutateAsync: async (on: boolean) => {
      state.ai.push(on)
    },
    isPending: false,
  }),
}))

function account(over: Partial<Account> = {}) {
  state.account = {
    status: "active",
    ai_enabled: false,
    ai_ends_at: null,
    spending_limit_usd: null,
    period: { start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z" },
    usage: [{ feature: "build_minutes", cents: 120 }],
    credit_used_cents: 0,
    overage_cents: 0,
    projected_total_cents: 500,
    payment_methods: [
      {
        brand: "visa",
        last4: "4242",
        exp_month: 4,
        exp_year: 2030,
        default: true,
      },
    ],
    ...over,
  } satisfies Account
}

beforeEach(() => {
  state.owner = true
  state.limitSaves = []
  state.limitAnswer = { accepted: false }
  state.ai = []
  state.invoices = []
  vi.stubGlobal("location", {
    ...window.location,
    search: "",
    assign: (url: string) => state.assigned.push(url),
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  state.assigned = []
})

it("shows included usage as dollars of five", async () => {
  account({ credit_used_cents: 300 })
  render(<BillingPage />)
  expect(
    await screen.findByText("Included usage: $3.00 of $5.00 used")
  ).toBeInTheDocument()
  expect(
    screen.getByText("$5 a month, including $5 of usage.")
  ).toBeInTheDocument()
})

it("rejects a limit with three decimals before sending it", async () => {
  account()
  render(<BillingPage />)
  await userEvent.type(screen.getByLabelText("Monthly limit (USD)"), "1.234")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  expect(
    await screen.findByText("Use at most two decimals.")
  ).toBeInTheDocument()
  expect(state.limitSaves).toEqual([])
})

it("sends a limit as dollars and null for none", async () => {
  account({ spending_limit_usd: "25.00" })
  render(<BillingPage />)
  const input = screen.getByLabelText("Monthly limit (USD)")
  expect(input).toHaveValue("25.00")
  await userEvent.clear(input)
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(state.limitSaves).toEqual([null]))
  expect(await screen.findByText("Saved.")).toBeInTheDocument()
})

it("says the change is being applied when the save is accepted", async () => {
  account()
  state.limitAnswer = { accepted: true }
  render(<BillingPage />)
  await userEvent.type(screen.getByLabelText("Monthly limit (USD)"), "10")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  expect(
    await screen.findByText("Saved. The change is being applied.")
  ).toBeInTheDocument()
  expect(state.limitSaves).toEqual(["10.00"])
})

it("says until when AI stays on after turning it off", async () => {
  account({ ai_enabled: true, ai_ends_at: "2026-12-01T00:00:00Z" })
  render(<BillingPage />)
  expect(await screen.findByText("On until 1 December")).toBeInTheDocument()
  expect(screen.getByRole("switch")).not.toBeChecked()
})

it("turns AI on and off from the switch", async () => {
  account({ ai_enabled: true })
  render(<BillingPage />)
  const sw = await screen.findByRole("switch")
  expect(sw).toBeChecked()
  await userEvent.click(sw)
  await waitFor(() => expect(state.ai).toEqual([false]))
})

it("hides the actions from an admin who isn't the owner", async () => {
  state.owner = false
  account()
  render(<BillingPage />)
  await screen.findByText("Payment method")
  expect(
    screen.queryByRole("button", { name: /Add a card|Replace card/ })
  ).toBeNull()
  expect(screen.queryByRole("switch")).toBeNull()
  expect(screen.queryByRole("button", { name: "Save" })).toBeNull()
  expect(
    screen.getByText("Visa ending 4242, expires 4/2030")
  ).toBeInTheDocument()
})

it("sends the owner to Stripe to replace the card", async () => {
  account()
  render(<BillingPage />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Replace card" })
  )
  await waitFor(() =>
    expect(state.assigned).toEqual(["https://stripe.test/setup"])
  )
})

it("offers to add a card when there is none", async () => {
  account({ payment_methods: [] })
  render(<BillingPage />)
  expect(
    await screen.findByRole("button", { name: "Add a card" })
  ).toBeInTheDocument()
  expect(screen.getByText("No card on file.")).toBeInTheDocument()
})

it("welcomes a card that was just added", async () => {
  account()
  vi.stubGlobal("location", { ...window.location, search: "?card=added" })
  render(<BillingPage />)
  expect(
    await screen.findByText(
      "Card added. It can take a few seconds to show here."
    )
  ).toBeInTheDocument()
})

it("lists usage and opens an invoice in a tab", async () => {
  account()
  state.invoices = [
    {
      id: "inv_1",
      period_start: "2026-10-01T00:00:00Z",
      period_end: "2026-11-01T00:00:00Z",
      total_cents: 700,
      status: "paid",
      plan: "cloud",
    },
  ]
  const tab = { opener: {}, location: { href: "" }, close: vi.fn() }
  const open = vi.fn(() => tab)
  vi.stubGlobal("open", open)
  render(<BillingPage />)
  expect(await screen.findByText("Builds")).toBeInTheDocument()
  expect(screen.getByText("$1.20")).toBeInTheDocument()
  expect(screen.getByText("$7.00")).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "View" }))
  await waitFor(() =>
    expect(tab.location.href).toBe("https://stripe.test/invoice")
  )
})
