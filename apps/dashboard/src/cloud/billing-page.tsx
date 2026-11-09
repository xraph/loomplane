// Settings > Billing: the plan and what it has used, the AI add-on, the
// spending limit, the card, this month's usage and past invoices. The owner
// changes things; an admin sees every section and can change none of it.
import { CheckIcon } from "lucide-react"
import { type FormEvent, useEffect, useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import {
  FieldProblem,
  SettingsCardFooter,
  SettingsSection,
} from "@/components/settings-section"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useOrganization } from "@/lib/organization"

import {
  type Account,
  type BillingStatus,
  invalidateBilling,
  useBillingAccount,
  useInvoiceLink,
  useInvoices,
  useSetAI,
  useSetSpendingLimit,
  useSetupSession,
} from "./client"
import { dollars, monthDay, parseUSD, shortDate, wireDollars } from "./money"

// The base plan's included usage, in cents.
const INCLUDED_CENTS = 500
const AI_INCLUDED_CENTS = 1000

const statusLabel: Record<BillingStatus, string> = {
  pending_payment: "Needs a card",
  active: "Active",
  past_due: "Payment overdue",
  limited: "Limit reached",
}

const featureLabel: Record<string, string> = {
  build_minutes: "Builds",
  compute_vcpu_hours: "Compute (vCPU)",
  compute_gb_ram_hours: "Compute (memory)",
  storage_gb_months: "Storage",
  egress_gb: "Network egress",
  ai_usd: "AI on Loomplane's keys",
}

/** BillingPage is the workspace's Settings > Billing page. */
export function BillingPage() {
  const account = useBillingAccount()
  const { can } = useOrganization()
  const owner = can("organization:administer")
  const added =
    new URLSearchParams(window.location.search).get("card") === "added"
  const denied = !!account.error && statusOf(account.error) === 403

  // Stripe tells us about a new card a moment after the person is back, so
  // look again for a little while.
  const waiting =
    added && (!account.data || account.data.status === "pending_payment")
  useEffect(() => {
    if (!waiting) return
    let n = 0
    const id = window.setInterval(() => {
      n++
      invalidateBilling()
      if (n >= 10) window.clearInterval(id)
    }, 3000)
    return () => window.clearInterval(id)
  }, [waiting])

  return (
    <>
      <SiteHeader crumbs={[{ label: "Billing" }]} />
      <Page className="gap-6">
        <PageHeading
          title="Billing"
          description="Your plan, what this workspace has used this month, and the card it pays with."
        />
        {added && (
          <p
            role="status"
            className="flex items-center gap-1.5 text-[13px] text-overdye-ink"
          >
            <CheckIcon className="size-4 shrink-0" />
            Card added. It can take a few seconds to show here.
          </p>
        )}
        {denied && (
          <p className="text-[13.5px] text-muted-foreground">
            Only the workspace owner and admins can see billing.
          </p>
        )}
        {!!account.error && !denied && (
          <QueryError
            error={account.error}
            fallback="Billing couldn't be loaded."
            retry={() => {
              account.refetch()
              return Promise.resolve()
            }}
          />
        )}
        {!account.data && !account.error && (
          <Skeleton
            data-testid="billing-loading"
            className="h-56 w-full rounded-[14px]"
          />
        )}
        {account.data && (
          <>
            <PlanSection account={account.data} />
            <AISection account={account.data} owner={owner} />
            <LimitSection account={account.data} owner={owner} />
            <CardSection account={account.data} owner={owner} />
            <UsageSection account={account.data} />
            <InvoicesSection />
          </>
        )}
      </Page>
    </>
  )
}

function PlanSection({ account }: { account: Account }) {
  const used = account.base_credit_used_cents
  return (
    <SettingsSection
      title="Plan"
      description="One plan, billed monthly. The included usage covers builds, compute, storage and network."
    >
      <Panel className="gap-3 p-4 md:p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-[13.5px] font-semibold">Loomplane Cloud</h3>
          <Badge
            variant={account.status === "active" ? "secondary" : "outline"}
          >
            {statusLabel[account.status]}
          </Badge>
        </div>
        <p className="text-[13px] text-muted-foreground">
          $5 a month, including $5 of usage.
        </p>
        <Progress
          aria-label="Included usage"
          value={Math.min(100, (used / INCLUDED_CENTS) * 100)}
        />
        <p className="font-mono text-[12.5px] tabular-nums">
          {`Included usage: ${dollars(used)} of ${dollars(INCLUDED_CENTS)} used`}
        </p>
        {account.overage_cents > 0 && (
          <p className="text-[12.5px] text-muted-foreground">
            Past the included usage: {dollars(account.overage_cents)} so far.
          </p>
        )}
        <p className="text-[12.5px] text-muted-foreground">
          On track for {dollars(account.projected_total_cents)} this month, from{" "}
          {shortDate(account.period.start)}.
        </p>
        {account.status === "past_due" && (
          <p role="alert" className="text-[12.5px] text-destructive">
            A payment failed. Pay the invoice below, or replace the card, to
            keep work running.
          </p>
        )}
        {account.status === "limited" && (
          <p role="alert" className="text-[12.5px] text-destructive">
            The spending limit is reached, so work that costs money has stopped.
            Raise the limit, or wait for next month.
          </p>
        )}
      </Panel>
    </SettingsSection>
  )
}

function AISection({ account, owner }: { account: Account; owner: boolean }) {
  const setAI = useSetAI()
  const [problem, setProblem] = useState("")
  const label = useId()
  // Off but still running to the end of the month reads as off.
  const on = account.ai_enabled && !account.ai_ends_at
  const ending = account.ai_enabled && account.ai_ends_at
  const toggle = async (next: boolean) => {
    setProblem("")
    try {
      await setAI.mutateAsync(next)
    } catch (err) {
      setProblem(messageOf(err, "The AI add-on wasn't changed."))
    }
  }
  return (
    <SettingsSection
      title="AI add-on"
      description="Lets the agent and Loomplane's other AI features run on Loomplane's model keys, so you don't bring your own."
    >
      <Panel className="gap-3 p-4 md:p-5">
        <div className="flex items-center justify-between gap-4">
          <div className="grid gap-1">
            <p id={label} className="text-[13.5px] font-semibold">
              AI add-on
            </p>
            <p className="text-[12.5px] text-muted-foreground">
              $10 a month, including $10 of AI on Loomplane&apos;s model keys.
            </p>
            {ending && account.ai_ends_at && (
              <p className="text-[12.5px] text-muted-foreground">
                On until {monthDay(account.ai_ends_at)}
              </p>
            )}
          </div>
          {owner && (
            <Switch
              aria-labelledby={label}
              checked={on}
              disabled={setAI.isPending}
              onCheckedChange={(next) => void toggle(next)}
            />
          )}
          {!owner && (
            <Badge variant="outline">{on || ending ? "On" : "Off"}</Badge>
          )}
        </div>
        {account.ai_enabled && (
          <>
            <Progress
              aria-label="Included AI"
              value={Math.min(
                100,
                (account.ai_credit_used_cents / AI_INCLUDED_CENTS) * 100
              )}
            />
            <p className="font-mono text-[12.5px] tabular-nums">
              {`Included AI: ${dollars(account.ai_credit_used_cents)} of ${dollars(AI_INCLUDED_CENTS)} used`}
            </p>
          </>
        )}
        <FieldProblem text={problem} />
      </Panel>
    </SettingsSection>
  )
}

function LimitSection({
  account,
  owner,
}: {
  account: Account
  owner: boolean
}) {
  const save = useSetSpendingLimit()
  const field = useId()
  const [value, setValue] = useState(account.spending_limit_usd ?? "")
  const [problem, setProblem] = useState("")
  const [saved, setSaved] = useState("")
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    setSaved("")
    const parsed = parseUSD(value)
    if ("problem" in parsed) {
      setProblem(parsed.problem)
      return
    }
    try {
      const out = await save.mutateAsync(
        parsed.cents === null ? null : wireDollars(parsed.cents)
      )
      setSaved(out.accepted ? "Saved. The change is being applied." : "Saved.")
    } catch (err) {
      setProblem(messageOf(err, "The spending limit wasn't saved."))
    }
  }
  const help =
    "Caps what you pay past the included usage each month. Loomplane checks it every 15 minutes, so a little more can run before work stops."
  return (
    <SettingsSection
      title="Spending limit"
      description="Optional. Without one, usage past the included amount is billed as it comes."
    >
      <Panel className="gap-0 p-0">
        <form onSubmit={(e) => void submit(e)} className="grid">
          <div className="grid gap-3 px-4 py-4 md:px-5">
            {owner ? (
              <Field>
                <FieldLabel htmlFor={field}>Monthly limit (USD)</FieldLabel>
                <Input
                  id={field}
                  inputMode="decimal"
                  value={value}
                  onChange={(e) => {
                    setValue(e.target.value)
                    setSaved("")
                  }}
                  placeholder="No limit"
                  aria-invalid={!!problem}
                  className="max-w-[200px] tabular-nums"
                />
                <FieldProblem text={problem} />
              </Field>
            ) : (
              <p className="text-[13.5px]">
                {account.spending_limit_usd === null
                  ? "No limit"
                  : `$${account.spending_limit_usd} a month`}
              </p>
            )}
            <p className="text-[12.5px] text-muted-foreground">{help}</p>
          </div>
          {owner && (
            <SettingsCardFooter>
              {saved ? (
                <p
                  role="status"
                  className="flex items-center gap-1.5 text-[12.5px] text-overdye-ink"
                >
                  <CheckIcon className="size-4 shrink-0" />
                  {saved}
                </p>
              ) : (
                <p className="text-[12.5px] text-muted-foreground">
                  Leave it empty for no limit.
                </p>
              )}
              <Button type="submit" size="sm" disabled={save.isPending}>
                {save.isPending && <Spinner />}
                Save
              </Button>
            </SettingsCardFooter>
          )}
        </form>
      </Panel>
    </SettingsSection>
  )
}

function CardSection({ account, owner }: { account: Account; owner: boolean }) {
  const setup = useSetupSession()
  const [problem, setProblem] = useState("")
  const card =
    account.payment_methods.find((m) => m.default) ?? account.payment_methods[0]
  const start = async () => {
    setProblem("")
    try {
      const { url } = await setup.mutateAsync()
      window.location.assign(url)
    } catch (err) {
      setProblem(messageOf(err, "Couldn't start adding a card. Try again."))
    }
  }
  return (
    <SettingsSection
      title="Payment method"
      description="The card Loomplane charges each month. Stripe holds it; Loomplane never sees the number."
    >
      <Panel className="gap-3 p-4 md:p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-[13.5px]">
            {card
              ? `${cardBrand(card.brand)} ending ${card.last4}, expires ${card.exp_month}/${card.exp_year}`
              : "No card on file."}
          </p>
          {owner && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => void start()}
              disabled={setup.isPending}
            >
              {setup.isPending && <Spinner />}
              {card ? "Replace card" : "Add a card"}
            </Button>
          )}
        </div>
        <FieldProblem text={problem} />
      </Panel>
    </SettingsSection>
  )
}

function cardBrand(brand: string): string {
  if (!brand) return "Card"
  return brand.charAt(0).toUpperCase() + brand.slice(1)
}

function UsageSection({ account }: { account: Account }) {
  return (
    <SettingsSection
      title="Usage this month"
      description={`From ${shortDate(account.period.start)}. Dollars at the rates in force when the usage ran.`}
    >
      <Panel className="p-0">
        {account.usage.length === 0 ? (
          <p className="px-4 py-4 text-[13px] text-muted-foreground md:px-5">
            No usage yet this month.
          </p>
        ) : (
          <Table>
            <TableBody>
              {account.usage.map((u) => (
                <TableRow key={u.feature}>
                  <TableCell>{featureLabel[u.feature] ?? u.feature}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">
                    {dollars(u.cents)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </SettingsSection>
  )
}

const invoiceStatus: Record<string, string> = {
  paid: "Paid",
  open: "Open",
  pending: "Pending",
  draft: "Draft",
  void: "Void",
  voided: "Void",
  uncollectible: "Uncollectible",
}

function InvoicesSection() {
  const invoices = useInvoices()
  const link = useInvoiceLink()
  const [problem, setProblem] = useState("")
  const view = async (id: string) => {
    setProblem("")
    // Opened now, in the click, so a pop-up blocker lets it through.
    const tab = window.open("", "_blank")
    try {
      const { url } = await link.mutateAsync(id)
      if (tab) {
        tab.opener = null
        tab.location.href = url
      } else {
        window.location.assign(url)
      }
    } catch (err) {
      tab?.close()
      setProblem(messageOf(err, "Couldn't open the invoice. Try again."))
    }
  }
  return (
    <SettingsSection
      title="Invoices"
      description="One a month, for the month that just ended."
    >
      <Panel className="p-0">
        {invoices.error ? (
          <div className="p-4">
            <QueryError
              error={invoices.error}
              fallback="Invoices couldn't be loaded."
              retry={() => {
                invoices.refetch()
                return Promise.resolve()
              }}
            />
          </div>
        ) : !invoices.data ? (
          <Skeleton className="h-24 w-full rounded-[14px]" />
        ) : invoices.data.length === 0 ? (
          <p className="px-4 py-4 text-[13px] text-muted-foreground md:px-5">
            No invoices yet. The first comes at the end of the month.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Date</TableHead>
                <TableHead className="text-right">Total</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {invoices.data.map((inv) => (
                <TableRow key={inv.id}>
                  <TableCell>{shortDate(inv.period_end)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">
                    {dollars(inv.total_cents)}
                  </TableCell>
                  <TableCell>
                    {invoiceStatus[inv.status] ?? inv.status}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      onClick={() => void view(inv.id)}
                    >
                      View
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {problem && (
          <div className="px-4 pb-3">
            <FieldProblem text={problem} />
          </div>
        )}
      </Panel>
    </SettingsSection>
  )
}
