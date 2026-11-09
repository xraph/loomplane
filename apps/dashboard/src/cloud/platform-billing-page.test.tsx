import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import type { PlatformInvoice } from "./client"
import { PlatformBillingPage } from "./platform-billing-page"

const state = vi.hoisted(() => ({
  invoices: [] as unknown[],
  puts: [] as { feature: string; usd: string }[],
  refunds: [] as { invoice: string; usd: string | null; key: string }[],
  settles: [] as unknown[],
  credits: [] as unknown[],
  refundError: undefined as unknown,
  overviewError: undefined as unknown,
}))

vi.mock("@/components/route-feedback", () => ({
  PageNotFound: () => <p>Page not found</p>,
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("./client", () => {
  const read = <T,>(data: T | undefined, error?: unknown) => ({
    data,
    error,
    loading: false,
    refetch: () => {},
  })
  return {
    usePlatformOverview: () =>
      state.overviewError
        ? read(undefined, state.overviewError)
        : read({ stripe_mode: "test", plans: [] }),
    useRates: () =>
      read([
        { feature: "build_minutes", unit: "minute", usd_per_unit: "0.010000" },
      ]),
    usePlatformWorkspaces: () =>
      read([
        {
          org_id: "org_1",
          name: "Acme",
          status: "active",
          period_total_cents: 900,
          overage_cents: 400,
          ai_enabled: true,
        },
      ]),
    usePlatformInvoices: () => read(state.invoices),
    useSetRate: () => ({
      mutateAsync: async (a: { feature: string; usd: string }) => {
        state.puts.push(a)
      },
      isPending: false,
    }),
    useRefund: () => ({
      mutateAsync: async (a: {
        invoice: string
        usd: string | null
        key: string
      }) => {
        state.refunds.push(a)
        if (state.refundError) throw state.refundError
        return { full: false, replay: false }
      },
      isPending: false,
    }),
    useSettleRefund: () => ({
      mutateAsync: async (a: unknown) => {
        state.settles.push(a)
      },
      isPending: false,
    }),
    useGrantCredit: () => ({
      mutateAsync: async (a: unknown) => {
        state.credits.push(a)
      },
      isPending: false,
    }),
  }
})

function invoice(over: Partial<PlatformInvoice> = {}): PlatformInvoice {
  return {
    id: "inv_1",
    period_start: "2026-10-01T00:00:00Z",
    period_end: "2026-11-01T00:00:00Z",
    total_cents: 1000,
    status: "paid",
    plan: "cloud",
    refunded_cents: 0,
    refundable_cents: 1000,
    pending_refunds: [],
    ...over,
  }
}

const pendingRefund = (over = {}) => ({
  refund_key: "key-1",
  cents: 300,
  created_at: "2026-11-03T00:00:00Z",
  resendable: true,
  needs_settlement: false,
  ...over,
})

beforeEach(() => {
  state.invoices = [invoice()]
  state.puts = []
  state.refunds = []
  state.settles = []
  state.credits = []
  state.refundError = undefined
  state.overviewError = undefined
})
afterEach(cleanup)

const openInvoices = async () => {
  render(<PlatformBillingPage />)
  await userEvent.click(
    await screen.findByRole("button", { name: "Invoices for Acme" })
  )
}

const refundInDialog = () =>
  userEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Refund" })
  )

it("shows Stripe's mode, the rates and the workspaces", async () => {
  render(<PlatformBillingPage />)
  expect(await screen.findByText("Stripe test mode")).toBeInTheDocument()
  expect(screen.getByText("Builds")).toBeInTheDocument()
  expect(screen.getByText("$0.010000")).toBeInTheDocument()
  expect(screen.getByText("Acme")).toBeInTheDocument()
})

it("is a not found page for anyone but the platform owner", () => {
  state.overviewError = Object.assign(new Error("not found"), {
    statusCode: 404,
  })
  render(<PlatformBillingPage />)
  expect(screen.getByText("Page not found")).toBeInTheDocument()
})

it("rejects a rate with seven decimals", async () => {
  render(<PlatformBillingPage />)
  await userEvent.click(await screen.findByRole("button", { name: "Edit" }))
  const input = screen.getByLabelText(/Price per minute/)
  await userEvent.clear(input)
  await userEvent.type(input, "0.0123456")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  expect(
    await screen.findByText("Use at most six decimals.")
  ).toBeInTheDocument()
  expect(state.puts).toEqual([])
})

it("saves a rate in place", async () => {
  render(<PlatformBillingPage />)
  await userEvent.click(await screen.findByRole("button", { name: "Edit" }))
  const input = screen.getByLabelText(/Price per minute/)
  await userEvent.clear(input)
  await userEvent.type(input, "0.012345")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(state.puts).toEqual([{ feature: "build_minutes", usd: "0.012345" }])
  )
})

it("refunds with a new key for each dialog", async () => {
  await openInvoices()
  await userEvent.click(await screen.findByRole("button", { name: "Refund" }))
  await userEvent.type(screen.getByLabelText("Amount (USD)"), "3")
  await refundInDialog()
  await waitFor(() => expect(state.refunds).toHaveLength(1))
  expect(state.refunds[0]).toMatchObject({ invoice: "inv_1", usd: "3.00" })

  await userEvent.click(await screen.findByRole("button", { name: "Refund" }))
  await refundInDialog()
  await waitFor(() => expect(state.refunds).toHaveLength(2))
  expect(state.refunds[1].usd).toBeNull()
  expect(state.refunds[1].key).not.toBe(state.refunds[0].key)
})

it("refuses more than what is left to refund", async () => {
  await openInvoices()
  await userEvent.click(await screen.findByRole("button", { name: "Refund" }))
  await userEvent.type(screen.getByLabelText("Amount (USD)"), "10.01")
  await refundInDialog()
  expect(await screen.findByText("Enter at most $10.00.")).toBeInTheDocument()
  expect(state.refunds).toEqual([])
})

it("closes a refund Stripe didn't confirm and says to retry the same one", async () => {
  state.refundError = Object.assign(
    new Error(
      "Stripe didn't confirm this refund. Retry this same refund from the invoice."
    ),
    { statusCode: 503 }
  )
  await openInvoices()
  await userEvent.click(await screen.findByRole("button", { name: "Refund" }))
  await refundInDialog()
  expect(
    await screen.findByText(/Retry this same refund from the invoice/)
  ).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
})

it("retries a pending refund with its stored key and offers no new refund", async () => {
  state.invoices = [invoice({ pending_refunds: [pendingRefund()] })]
  await openInvoices()
  expect(
    await screen.findByText(/Stripe hasn't confirmed it/)
  ).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Refund" })).toBeNull()
  await userEvent.click(
    screen.getByRole("button", { name: "Retry this refund" })
  )
  await waitFor(() =>
    expect(state.refunds).toEqual([
      { invoice: "inv_1", usd: "3.00", key: "key-1" },
    ])
  )
})

const stale = () => [
  invoice({
    pending_refunds: [
      pendingRefund({ resendable: false, needs_settlement: true }),
    ],
  }),
]

it("settles an old refund as dropped after a confirm", async () => {
  state.invoices = stale()
  await openInvoices()
  expect(screen.queryByRole("button", { name: "Retry this refund" })).toBeNull()
  await userEvent.click(await screen.findByRole("button", { name: "Settle" }))
  const dialog = screen.getByRole("dialog")
  await userEvent.click(
    within(dialog).getByLabelText("Stripe has no such refund")
  )
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Continue" })
  )
  expect(state.settles).toEqual([])
  expect(within(dialog).getByText(/This can't be undone/)).toBeInTheDocument()
  await userEvent.click(within(dialog).getByRole("button", { name: "Confirm" }))
  await waitFor(() =>
    expect(state.settles).toEqual([
      { invoice: "inv_1", key: "key-1", made: false, stripeRefundId: "" },
    ])
  )
})

it("settles as made only with a Stripe refund id", async () => {
  state.invoices = stale()
  await openInvoices()
  await userEvent.click(await screen.findByRole("button", { name: "Settle" }))
  const dialog = screen.getByRole("dialog")
  const id = () => within(dialog).getByLabelText("Stripe refund id")
  await userEvent.type(id(), "nope")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Continue" })
  )
  expect(await within(dialog).findByText(/starts with re_/)).toBeInTheDocument()
  await userEvent.clear(id())
  await userEvent.type(id(), "re_123abc")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Continue" })
  )
  await userEvent.click(within(dialog).getByRole("button", { name: "Confirm" }))
  await waitFor(() =>
    expect(state.settles).toEqual([
      {
        invoice: "inv_1",
        key: "key-1",
        made: true,
        stripeRefundId: "re_123abc",
      },
    ])
  )
})

it("adds credit to a workspace", async () => {
  await openInvoices()
  await userEvent.click(
    await screen.findByRole("button", { name: "Add credit" })
  )
  await userEvent.type(screen.getByLabelText("Amount (USD)"), "5")
  await userEvent.type(screen.getByLabelText("Note"), "goodwill")
  await userEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: "Add credit",
    })
  )
  await waitFor(() =>
    expect(state.credits).toEqual([
      { org: "org_1", usd: "5.00", note: "goodwill" },
    ])
  )
})
