// The cloud edition's API client. The billing routes are not in the public
// OpenAPI spec, so nothing here is generated: each call is a plain fetch with
// the session cookie and the workspace header, and each read is a small hook.
import { useCallback, useEffect, useState, useSyncExternalStore } from "react"

import { currentOrganization, ORG_HEADER } from "@/client/runtime"

const WORKSPACE = "/api/v1/billing"
const PLATFORM = "/api/v1/platform/billing"

export type BillingStatus =
  "pending_payment" | "active" | "past_due" | "limited"

export type Account = {
  status: BillingStatus
  ai_enabled: boolean
  ai_ends_at: string | null
  spending_limit_usd: string | null
  period: { start: string; end: string }
  usage: { feature: string; cents: number }[]
  // Each credit's use on its own: the plan's $5 of base usage, and the AI
  // add-on's $10 of AI (0 while the add-on doesn't cover the month).
  base_credit_used_cents: number
  ai_credit_used_cents: number
  overage_cents: number
  projected_total_cents: number
  payment_methods: {
    brand: string
    last4: string
    exp_month: number
    exp_year: number
    default: boolean
  }[]
}

export type Invoice = {
  id: string
  period_start: string
  period_end: string
  total_cents: number
  status: string
  plan: string
}

export type Rate = { feature: string; unit: string; usd_per_unit: string }

export type PlatformWorkspace = {
  org_id: string
  name: string
  status: BillingStatus
  period_total_cents: number
  overage_cents: number
  ai_enabled: boolean
}

export type PendingRefund = {
  refund_key: string
  cents: number
  created_at: string
  resendable: boolean
  needs_settlement: boolean
}

export type PlatformInvoice = Invoice & {
  refunded_cents: number
  refundable_cents: number
  pending_refunds: PendingRefund[]
}

export type Overview = {
  stripe_mode: string
  plans: { slug: string; base_cents: number; credit_cents: number }[]
}

type Failure = Error & { statusCode: number }

/**
 * cloudRequest sends one request and answers its status and decoded JSON
 * body (undefined when there is none). A non-2xx answer throws an error with
 * the server's sentence and its statusCode, so messageOf and statusOf read it.
 */
export async function cloudRequest<T>(
  path: string,
  init: { method?: string; body?: unknown } = {}
): Promise<{ status: number; data: T }> {
  const headers: Record<string, string> = { Accept: "application/json" }
  const org = currentOrganization()
  if (org) headers[ORG_HEADER] = org
  if (init.body !== undefined) headers["Content-Type"] = "application/json"
  const res = await fetch(path, {
    method: init.method ?? "GET",
    credentials: "same-origin",
    headers,
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
  })
  const text = await res.text()
  let parsed: unknown
  try {
    parsed = text ? JSON.parse(text) : undefined
  } catch {
    parsed = undefined
  }
  if (!res.ok) {
    const b = (parsed ?? {}) as { message?: unknown; error?: unknown }
    const message =
      typeof b.message === "string" && b.message
        ? b.message
        : typeof b.error === "string" && b.error
          ? b.error
          : `HTTP ${res.status}`
    throw Object.assign(new Error(message), {
      statusCode: res.status,
    }) as Failure
  }
  return { status: res.status, data: parsed as T }
}

/** cloudFetch is cloudRequest for a caller that only wants the body. */
export async function cloudFetch<T>(
  path: string,
  init?: { method?: string; body?: unknown }
): Promise<T> {
  return (await cloudRequest<T>(path, init)).data
}

// Every read refetches when anything changes, so a card added in one place
// lifts the gate in another.
let version = 0
const listeners = new Set<() => void>()
const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}
const snapshot = () => version

/** invalidateBilling makes every mounted billing read fetch again. */
export function invalidateBilling(): void {
  version++
  listeners.forEach((l) => l())
}

export type Resource<T> = {
  data: T | undefined
  error: unknown
  loading: boolean
  refetch: () => void
}

function useResource<T>(path: string | null): Resource<T> {
  const v = useSyncExternalStore(subscribe, snapshot)
  const [tick, setTick] = useState(0)
  const [state, setState] = useState<{
    data?: T
    error?: unknown
    loading: boolean
  }>({ loading: path !== null })
  useEffect(() => {
    if (path === null) return
    let live = true
    cloudFetch<T>(path).then(
      (data) => {
        if (live) setState({ data, loading: false })
      },
      (error: unknown) => {
        if (live) setState((s) => ({ data: s.data, error, loading: false }))
      }
    )
    return () => {
      live = false
    }
  }, [path, v, tick])
  const refetch = useCallback(() => setTick((t) => t + 1), [])
  return {
    data: state.data,
    error: state.error,
    loading: state.loading,
    refetch,
  }
}

export type Action<A, R> = {
  mutateAsync: (arg: A) => Promise<R>
  isPending: boolean
}

function useAction<A, R>(run: (arg: A) => Promise<R>): Action<A, R> {
  const [pending, setPending] = useState(0)
  const mutateAsync = useCallback(
    async (arg: A) => {
      setPending((n) => n + 1)
      try {
        return await run(arg)
      } finally {
        setPending((n) => n - 1)
      }
    },
    // The callers pass module-level functions, which never change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    []
  )
  return { mutateAsync, isPending: pending > 0 }
}

// Workspace reads and writes.

export const useBillingStatus = () =>
  useResource<{ status: BillingStatus }>(`${WORKSPACE}/status`)
export const useBillingAccount = () =>
  useResource<Account>(`${WORKSPACE}/account`)
export const useInvoices = () => useResource<Invoice[]>(`${WORKSPACE}/invoices`)

export const useSetupSession = () =>
  useAction<void, { url: string }>(() =>
    cloudFetch(`${WORKSPACE}/setup-session`, { method: "POST" })
  )

/** What a spending limit save answers: accepted is the 202, saved and still being applied. */
export type LimitSaved = { accepted: boolean }

export const useSetSpendingLimit = () =>
  useAction<string | null, LimitSaved>(async (limit) => {
    const { status } = await cloudRequest(`${WORKSPACE}/spending-limit`, {
      method: "PUT",
      body: { limit_usd: limit },
    })
    invalidateBilling()
    return { accepted: status === 202 }
  })

export const useSetAI = () =>
  useAction<boolean, unknown>(async (enabled) => {
    const out = await cloudFetch(`${WORKSPACE}/ai`, {
      method: "PUT",
      body: { enabled },
    })
    invalidateBilling()
    return out
  })

export const useInvoiceLink = () =>
  useAction<string, { url: string }>((id) =>
    cloudFetch(`${WORKSPACE}/invoices/${encodeURIComponent(id)}/link`)
  )

// The platform owner's reads and writes.

/** platformAccess asks whether the signed-in person is the platform owner. */
export async function platformAccess(): Promise<boolean> {
  try {
    const { status } = await cloudRequest(`${PLATFORM}/access`)
    return status === 204
  } catch (err) {
    // A network failure says nothing about who the person is.
    if (!(err && typeof err === "object" && "statusCode" in err)) throw err
    return false
  }
}

export const usePlatformOverview = () =>
  useResource<Overview>(`${PLATFORM}/overview`)
export const useRates = () => useResource<Rate[]>(`${PLATFORM}/rates`)
export const usePlatformWorkspaces = () =>
  useResource<PlatformWorkspace[]>(`${PLATFORM}/workspaces`)
export const usePlatformInvoices = (org: string | null) =>
  useResource<PlatformInvoice[]>(
    org === null
      ? null
      : `${PLATFORM}/workspaces/${encodeURIComponent(org)}/invoices`
  )

export const useSetRate = () =>
  useAction<{ feature: string; usd: string }, Rate>(async (a) => {
    const out = await cloudFetch<Rate>(
      `${PLATFORM}/rates/${encodeURIComponent(a.feature)}`,
      { method: "PUT", body: { usd_per_unit: a.usd } }
    )
    invalidateBilling()
    return out
  })

export const useGrantCredit = () =>
  useAction<{ org: string; usd: string; note: string }, void>(async (a) => {
    await cloudFetch(
      `${PLATFORM}/workspaces/${encodeURIComponent(a.org)}/credit`,
      {
        method: "POST",
        body: { usd: a.usd, note: a.note },
      }
    )
    invalidateBilling()
  })

/** A refund request. usd null is what is left of the invoice. */
export type RefundRequest = {
  invoice: string
  usd: string | null
  key: string
}

export const useRefund = () =>
  useAction<RefundRequest, { full: boolean; replay: boolean }>(async (a) => {
    try {
      return await cloudFetch(
        `${PLATFORM}/invoices/${encodeURIComponent(a.invoice)}/refund`,
        { method: "POST", body: { usd: a.usd, refund_key: a.key } }
      )
    } finally {
      // A refund Stripe never confirmed shows as pending afterwards.
      invalidateBilling()
    }
  })

export type SettleRequest = {
  invoice: string
  key: string
  made: boolean
  stripeRefundId: string
}

export const useSettleRefund = () =>
  useAction<SettleRequest, void>(async (a) => {
    await cloudFetch(
      `${PLATFORM}/invoices/${encodeURIComponent(a.invoice)}/refunds/${encodeURIComponent(a.key)}/settle`,
      {
        method: "POST",
        body: a.made
          ? { outcome: "made", stripe_refund_id: a.stripeRefundId }
          : { outcome: "dropped" },
      }
    )
    invalidateBilling()
  })
