// The platform owner's billing page: Stripe's mode, the price of each metered
// feature, every workspace's month so far, and per workspace its invoices
// with refunds and credits. The server shows it to the platform owner alone.
import { useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { PageNotFound } from "@/components/route-feedback"
import { FieldProblem, SettingsSection } from "@/components/settings-section"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

import {
  type PendingRefund,
  type PlatformInvoice,
  type PlatformWorkspace,
  type Rate,
  useGrantCredit,
  usePlatformInvoices,
  usePlatformOverview,
  usePlatformWorkspaces,
  useRates,
  useRefund,
  useSetRate,
  useSettleRefund,
} from "./client"
import {
  dollars,
  parseAmount,
  parseRate,
  shortDate,
  wireDollars,
} from "./money"

const MAX_CREDIT_CENTS = 100_000
const MAX_NOTE = 500
const STRIPE_REFUND_ID = /^(re|pyr)_[A-Za-z0-9]{1,250}$/

const featureLabel: Record<string, string> = {
  build_minutes: "Builds",
  compute_vcpu_hours: "Compute (vCPU)",
  compute_gb_ram_hours: "Compute (memory)",
  storage_gb_months: "Storage",
  egress_gb: "Network egress",
  ai_usd: "AI on Loomplane's keys",
}

const statusLabel: Record<string, string> = {
  pending_payment: "Needs a card",
  active: "Active",
  past_due: "Payment overdue",
  limited: "Limit reached",
}

/** PlatformBillingPage is the platform owner's billing page. */
export function PlatformBillingPage() {
  const overview = usePlatformOverview()
  // The server answers anyone but the platform owner as if the page weren't
  // there, and so does this.
  if (overview.error && statusOf(overview.error) === 404) {
    return <PageNotFound />
  }
  return (
    <>
      <SiteHeader crumbs={[{ label: "Platform billing" }]} />
      <Page className="gap-6">
        <PageHeading
          title="Platform billing"
          description="What each metered feature costs, how every workspace stands this month, and the refunds and credits you've made."
          actions={
            overview.data && (
              <Badge
                variant={
                  overview.data.stripe_mode === "live" ? "default" : "outline"
                }
              >
                {overview.data.stripe_mode === "live"
                  ? "Stripe live mode"
                  : "Stripe test mode"}
              </Badge>
            )
          }
        />
        <RatesSection />
        <WorkspacesSection />
      </Page>
    </>
  )
}

function RatesSection() {
  const rates = useRates()
  return (
    <SettingsSection
      title="Rates"
      description="Dollars per unit, with up to six decimals. A new rate applies to usage from now on; usage already recorded keeps the price it ran at."
    >
      <Panel className="p-0">
        {rates.error ? (
          <div className="p-4">
            <QueryError
              error={rates.error}
              fallback="The rates couldn't be loaded."
              retry={() => {
                rates.refetch()
                return Promise.resolve()
              }}
            />
          </div>
        ) : !rates.data ? (
          <Skeleton className="h-40 w-full rounded-[14px]" />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Feature</TableHead>
                <TableHead>Unit</TableHead>
                <TableHead>Price per unit</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rates.data.map((r) => (
                <RateRow key={r.feature} rate={r} />
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </SettingsSection>
  )
}

function RateRow({ rate }: { rate: Rate }) {
  const set = useSetRate()
  const field = useId()
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(rate.usd_per_unit)
  const [problem, setProblem] = useState("")
  const save = async () => {
    const parsed = parseRate(value)
    if ("problem" in parsed) {
      setProblem(parsed.problem)
      return
    }
    setProblem("")
    try {
      await set.mutateAsync({ feature: rate.feature, usd: parsed.usd })
      setEditing(false)
    } catch (err) {
      setProblem(messageOf(err, "The rate wasn't saved."))
    }
  }
  return (
    <TableRow>
      <TableCell>{featureLabel[rate.feature] ?? rate.feature}</TableCell>
      <TableCell className="text-muted-foreground">{rate.unit}</TableCell>
      <TableCell className="font-mono tabular-nums">
        {editing ? (
          <div className="grid gap-1">
            <label htmlFor={field} className="sr-only">
              {`Price per ${rate.unit} for ${featureLabel[rate.feature] ?? rate.feature}`}
            </label>
            <Input
              id={field}
              inputMode="decimal"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              aria-invalid={!!problem}
              className="h-8 max-w-[160px] tabular-nums"
            />
            <FieldProblem text={problem} />
          </div>
        ) : (
          `$${rate.usd_per_unit}`
        )}
      </TableCell>
      <TableCell className="text-right">
        {editing ? (
          <span className="inline-flex gap-1">
            <Button
              type="button"
              size="sm"
              onClick={() => void save()}
              disabled={set.isPending}
            >
              {set.isPending && <Spinner />}
              Save
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
                setEditing(false)
                setValue(rate.usd_per_unit)
                setProblem("")
              }}
            >
              Cancel
            </Button>
          </span>
        ) : (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => {
              setValue(rate.usd_per_unit)
              setEditing(true)
            }}
          >
            Edit
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}

function WorkspacesSection() {
  const workspaces = usePlatformWorkspaces()
  const [picked, setPicked] = useState<PlatformWorkspace | null>(null)
  return (
    <SettingsSection
      title="Workspaces"
      description="Each workspace's month so far. Open one for its invoices, refunds and credit."
    >
      <Panel className="p-0">
        {workspaces.error ? (
          <div className="p-4">
            <QueryError
              error={workspaces.error}
              fallback="The workspaces couldn't be loaded."
              retry={() => {
                workspaces.refetch()
                return Promise.resolve()
              }}
            />
          </div>
        ) : !workspaces.data ? (
          <Skeleton className="h-40 w-full rounded-[14px]" />
        ) : workspaces.data.length === 0 ? (
          <p className="px-4 py-4 text-[13px] text-muted-foreground md:px-5">
            No workspace is billed yet.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Workspace</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">This month</TableHead>
                <TableHead className="text-right">Past included</TableHead>
                <TableHead>AI</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {workspaces.data.map((w) => (
                <TableRow key={w.org_id}>
                  <TableCell className="font-medium">{w.name}</TableCell>
                  <TableCell>{statusLabel[w.status] ?? w.status}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">
                    {dollars(w.period_total_cents)}
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">
                    {dollars(w.overage_cents)}
                  </TableCell>
                  <TableCell>{w.ai_enabled ? "On" : "Off"}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      aria-label={`Invoices for ${w.name}`}
                      onClick={() => setPicked(w)}
                    >
                      Invoices
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      {picked && (
        <WorkspaceInvoices
          key={picked.org_id}
          workspace={picked}
          onClose={() => setPicked(null)}
        />
      )}
    </SettingsSection>
  )
}

function WorkspaceInvoices({
  workspace,
  onClose,
}: {
  workspace: PlatformWorkspace
  onClose: () => void
}) {
  const invoices = usePlatformInvoices(workspace.org_id)
  const [crediting, setCrediting] = useState(false)
  const [notice, setNotice] = useState("")
  return (
    <Panel className="gap-3 p-4 md:p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13.5px] font-semibold">
          {`Invoices for ${workspace.name}`}
        </h3>
        <span className="inline-flex gap-1">
          <Button type="button" size="sm" onClick={() => setCrediting(true)}>
            Add credit
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={onClose}>
            Close
          </Button>
        </span>
      </div>
      {notice && (
        <p role="status" className="text-[12.5px] text-muted-foreground">
          {notice}
        </p>
      )}
      {invoices.error ? (
        <QueryError
          error={invoices.error}
          fallback="The invoices couldn't be loaded."
          retry={() => {
            invoices.refetch()
            return Promise.resolve()
          }}
        />
      ) : !invoices.data ? (
        <Skeleton className="h-24 w-full rounded-[10px]" />
      ) : invoices.data.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">No invoices yet.</p>
      ) : (
        <ul className="grid gap-3">
          {invoices.data.map((inv) => (
            <InvoiceItem key={inv.id} invoice={inv} onNotice={setNotice} />
          ))}
        </ul>
      )}
      {crediting && (
        <CreditDialog
          workspace={workspace}
          onClose={(done) => {
            setCrediting(false)
            if (done) setNotice(`Credit added for ${workspace.name}.`)
          }}
        />
      )}
    </Panel>
  )
}

function InvoiceItem({
  invoice: inv,
  onNotice,
}: {
  invoice: PlatformInvoice
  onNotice: (text: string) => void
}) {
  const [refunding, setRefunding] = useState(false)
  const pending = inv.pending_refunds
  return (
    <li className="grid gap-2 rounded-[10px] bg-subtle px-3.5 py-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-[13px]">
        <span>
          {shortDate(inv.period_end)}{" "}
          <span className="text-muted-foreground">{inv.status}</span>
        </span>
        <span className="font-mono tabular-nums">
          {dollars(inv.total_cents)}
          {inv.refunded_cents > 0 && (
            <span className="text-muted-foreground">
              {` (${dollars(inv.refunded_cents)} refunded)`}
            </span>
          )}
        </span>
      </div>
      {pending.map((p) => (
        <PendingRefundRow
          key={p.refund_key}
          invoice={inv}
          pending={p}
          onNotice={onNotice}
        />
      ))}
      {inv.refundable_cents > 0 &&
        (pending.length === 0 ? (
          <div>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => setRefunding(true)}
            >
              Refund
            </Button>
          </div>
        ) : (
          <p className="text-[12.5px] text-muted-foreground">
            Retry or settle the refund above before making another.
          </p>
        ))}
      {refunding && (
        <RefundDialog
          invoice={inv}
          onClose={(message) => {
            setRefunding(false)
            if (message) onNotice(message)
          }}
        />
      )}
    </li>
  )
}

// A refund Stripe never confirmed. A recent one is sent again under the key it
// was recorded with, never a new one, so Stripe can't pay it twice. An older
// one is past Stripe's idempotency window: someone checks Stripe and settles it.
function PendingRefundRow({
  invoice,
  pending,
  onNotice,
}: {
  invoice: PlatformInvoice
  pending: PendingRefund
  onNotice: (text: string) => void
}) {
  const refund = useRefund()
  const [problem, setProblem] = useState("")
  const [settling, setSettling] = useState(false)
  const retry = async () => {
    setProblem("")
    try {
      await refund.mutateAsync({
        invoice: invoice.id,
        usd: wireDollars(pending.cents),
        key: pending.refund_key,
      })
      onNotice("Refund sent.")
    } catch (err) {
      setProblem(
        messageOf(err, "The refund wasn't sent. Retry the same refund.")
      )
    }
  }
  return (
    <div className="grid gap-1.5 rounded-[8px] border border-dashed border-border px-3 py-2">
      <p className="text-[12.5px]">
        {`Refund of ${dollars(pending.cents)} from ${shortDate(pending.created_at)}: Stripe hasn't confirmed it.`}
      </p>
      <div className="flex flex-wrap gap-1">
        {pending.resendable && !pending.needs_settlement && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => void retry()}
            disabled={refund.isPending}
          >
            {refund.isPending && <Spinner />}
            Retry this refund
          </Button>
        )}
        {pending.needs_settlement && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => setSettling(true)}
          >
            Settle
          </Button>
        )}
      </div>
      {pending.needs_settlement && (
        <p className="text-[12.5px] text-muted-foreground">
          It's too old to send again safely. Check the invoice in Stripe, then
          settle it as made or dropped.
        </p>
      )}
      <FieldProblem text={problem} />
      {settling && (
        <SettleDialog
          invoice={invoice}
          pending={pending}
          onClose={(message) => {
            setSettling(false)
            if (message) onNotice(message)
          }}
        />
      )}
    </div>
  )
}

function RefundDialog({
  invoice,
  onClose,
}: {
  invoice: PlatformInvoice
  onClose: (message?: string) => void
}) {
  const refund = useRefund()
  const field = useId()
  // One key for this dialog, so a retry of the same refund is the same refund.
  // A dialog opened again is a new refund and gets a new key.
  const [key] = useState(() => crypto.randomUUID())
  const [value, setValue] = useState("")
  const [problem, setProblem] = useState("")
  const submit = async () => {
    setProblem("")
    let usd: string | null = null
    if (value.trim() !== "") {
      const parsed = parseAmount(value, invoice.refundable_cents)
      if ("problem" in parsed) {
        setProblem(parsed.problem)
        return
      }
      usd = wireDollars(parsed.cents)
    }
    try {
      await refund.mutateAsync({ invoice: invoice.id, usd, key })
      onClose("Refund made.")
    } catch (err) {
      if (statusOf(err) === 503) {
        // Recorded and unconfirmed: the invoice now lists it to retry.
        onClose(
          messageOf(
            err,
            "Stripe didn't confirm this refund. Retry the same refund."
          )
        )
        return
      }
      setProblem(messageOf(err, "The refund wasn't made."))
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Refund this invoice</DialogTitle>
          <DialogDescription>
            {`Up to ${dollars(invoice.refundable_cents)} is left to refund. Leave the amount empty to refund all of it.`}
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor={field}>Amount (USD)</FieldLabel>
          <Input
            id={field}
            inputMode="decimal"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={wireDollars(invoice.refundable_cents)}
            aria-invalid={!!problem}
            className="max-w-[200px] tabular-nums"
          />
          <FieldProblem text={problem} />
        </Field>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={() => onClose()}>
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => void submit()}
            disabled={refund.isPending}
          >
            {refund.isPending && <Spinner />}
            Refund
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SettleDialog({
  invoice,
  pending,
  onClose,
}: {
  invoice: PlatformInvoice
  pending: PendingRefund
  onClose: (message?: string) => void
}) {
  const settle = useSettleRefund()
  const field = useId()
  const [made, setMade] = useState(true)
  const [stripeId, setStripeId] = useState("")
  const [confirming, setConfirming] = useState(false)
  const [problem, setProblem] = useState("")
  const next = () => {
    if (made && !STRIPE_REFUND_ID.test(stripeId.trim())) {
      setProblem("Enter the refund's id from Stripe. It starts with re_.")
      return
    }
    setProblem("")
    setConfirming(true)
  }
  const submit = async () => {
    setProblem("")
    try {
      await settle.mutateAsync({
        invoice: invoice.id,
        key: pending.refund_key,
        made,
        stripeRefundId: stripeId.trim(),
      })
      onClose(made ? "Refund settled as made." : "Refund settled as dropped.")
    } catch (err) {
      setConfirming(false)
      setProblem(messageOf(err, "The refund wasn't settled."))
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Settle this refund</DialogTitle>
          <DialogDescription>
            {`Check the invoice in Stripe for the ${dollars(pending.cents)} refund, then say what you found.`}
          </DialogDescription>
        </DialogHeader>
        {confirming ? (
          <p className="text-[13px]">
            {made
              ? `Count ${dollars(pending.cents)} as refunded, with Stripe refund ${stripeId.trim()}? This can't be undone.`
              : `Drop this refund and free ${dollars(pending.cents)} to refund again? Do this only if Stripe has no such refund. This can't be undone.`}
          </p>
        ) : (
          <div className="grid gap-3">
            <label className="flex items-center gap-2 text-[13px]">
              <input
                type="radio"
                name="settle"
                checked={made}
                onChange={() => setMade(true)}
              />
              Stripe made this refund
            </label>
            <label className="flex items-center gap-2 text-[13px]">
              <input
                type="radio"
                name="settle"
                checked={!made}
                onChange={() => setMade(false)}
              />
              Stripe has no such refund
            </label>
            {made && (
              <Field>
                <FieldLabel htmlFor={field}>Stripe refund id</FieldLabel>
                <Input
                  id={field}
                  value={stripeId}
                  onChange={(e) => setStripeId(e.target.value)}
                  placeholder="re_..."
                  spellCheck={false}
                  className="max-w-[320px] font-mono"
                />
              </Field>
            )}
          </div>
        )}
        <FieldProblem text={problem} />
        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => (confirming ? setConfirming(false) : onClose())}
          >
            {confirming ? "Back" : "Cancel"}
          </Button>
          {confirming ? (
            <Button
              type="button"
              onClick={() => void submit()}
              disabled={settle.isPending}
            >
              {settle.isPending && <Spinner />}
              Confirm
            </Button>
          ) : (
            <Button type="button" onClick={next}>
              Continue
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CreditDialog({
  workspace,
  onClose,
}: {
  workspace: PlatformWorkspace
  onClose: (done?: boolean) => void
}) {
  const grant = useGrantCredit()
  const amount = useId()
  const note = useId()
  const [value, setValue] = useState("")
  const [text, setText] = useState("")
  const [problem, setProblem] = useState("")
  const submit = async () => {
    const parsed = parseAmount(value, MAX_CREDIT_CENTS)
    if ("problem" in parsed) {
      setProblem(parsed.problem)
      return
    }
    setProblem("")
    try {
      await grant.mutateAsync({
        org: workspace.org_id,
        usd: wireDollars(parsed.cents),
        note: text.trim(),
      })
      onClose(true)
    } catch (err) {
      setProblem(messageOf(err, "The credit wasn't added."))
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add credit</DialogTitle>
          <DialogDescription>
            {`Takes this much off ${workspace.name}'s next invoice, once. It can't go below zero.`}
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor={amount}>Amount (USD)</FieldLabel>
          <Input
            id={amount}
            inputMode="decimal"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-invalid={!!problem}
            className="max-w-[200px] tabular-nums"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={note}>Note</FieldLabel>
          <Input
            id={note}
            value={text}
            maxLength={MAX_NOTE}
            onChange={(e) => setText(e.target.value)}
            placeholder="Why you're adding it"
          />
        </Field>
        <FieldProblem text={problem} />
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={() => onClose()}>
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => void submit()}
            disabled={grant.isPending}
          >
            {grant.isPending && <Spinner />}
            Add credit
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
