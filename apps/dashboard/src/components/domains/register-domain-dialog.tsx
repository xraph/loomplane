// Registering a domain in the organization's own Cloudflare account: find a
// name, review the price Cloudflare quotes for it, and approve exactly that
// price. Built to the redesign's RegisterDomain artboard.
import { useEffect, useMemo, useRef, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDomainRegistrationCheck,
  useDomainRegistrationExtensions,
  useDomainRegistrationSearch,
  useDomainRegistrationsApprove,
  useDomainRegistrationsList,
  useDomainRegistrationsPreview,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  DomainOffer,
  DomainOfferList,
  DomainPrice,
  Registration,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ConnectDialog } from "@/components/cloud-connections"
import { DomainStatePill } from "@/components/domains/domain-states"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import {
  formatPrice,
  isSet,
  offerLabel,
  offerStatus,
  offerTone,
  ownedNames,
  useClock,
} from "@/lib/domains"
import { cn } from "@/lib/utils"

export function RegisterDomainDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange(open: boolean): void
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <DialogContent
        showCloseButton={false}
        className="gap-0 overflow-x-hidden rounded-2xl p-0 sm:max-w-[600px]"
      >
        <DialogHeader className="gap-1 px-5 pt-[18px] pb-1.5">
          <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
            Register a domain
          </DialogTitle>
          <DialogDescription className="leading-normal">
            Bought with your Cloudflare account and billed to it. The domain is
            yours, and Loomplane manages its DNS.
          </DialogDescription>
        </DialogHeader>
        {open && <Flow onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

/** The Cloudflare accounts that may register domains. */
function registrarAccounts(connections: Connection[]): Connection[] {
  return connections.filter(
    (c) =>
      c.cloud === "cloudflare" &&
      c.status === "ready" &&
      c.details?.registrar === "granted"
  )
}

function accountID(c: Connection): string {
  return String(c.details?.account_id ?? "")
}

/** accountLabel is "{account name} · {account_id}". */
function accountLabel(c: Connection): string {
  const id = accountID(c)
  const name = c.account || c.name
  // Not in the mock: Cloudflare hid the account's name, so it's the ID.
  if (!id || name === id) return name || id
  return `${name} · ${id}`
}

function priceOf(q: DomainPrice | undefined): string {
  return q ? formatPrice(q.currency, q.registrationCost) : ""
}

const body = "flex flex-col gap-3 px-5 pt-3 pb-4"
const footer =
  "flex gap-2 border-t border-border/60 bg-subtle px-5 py-3 rounded-b-2xl"
const field =
  "flex flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80"
const control =
  "rounded-[9px] border border-input bg-background text-foreground outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"

type Step = "search" | "review" | "pending"

function Flow({ onClose }: { onClose: () => void }) {
  const connections = useQuery(useConnectionsList)
  const overview = useQuery(useDomainsOverview)
  const registrationList = useQuery(useDomainRegistrationsList)
  const preview = useMutation(useDomainRegistrationsPreview)
  const approve = useMutation(useDomainRegistrationsApprove)
  const invalidate = useInvalidate()

  const accounts = useMemo(
    () => registrarAccounts(connections.data?.connections ?? []),
    [connections.data]
  )
  const [picked, setPicked] = useState("")
  const account = accounts.find((c) => c.id === picked) ?? accounts[0]
  const [connecting, setConnecting] = useState(false)

  const [step, setStep] = useState<Step>("search")
  const [q, setQ] = useState("")
  // What a fresh check said about a name, over what the search said.
  const [checked, setChecked] = useState<Record<string, DomainOffer>>({})
  const [chosen, setChosen] = useState("")
  // The account the review was quoted on. It never follows the live list.
  const [quotedOn, setQuotedOn] = useState<Connection>()
  const stillEligible =
    !!quotedOn &&
    accounts.some(
      (c) => c.id === quotedOn.id && accountID(c) === accountID(quotedOn)
    )

  const [renew, setRenew] = useState(true)
  const [privacy, setPrivacy] = useState(true)
  const [agree, setAgree] = useState(false)
  const [quoted, setQuoted] = useState<Registration>()
  const [previewing, setPreviewing] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [buying, setBuying] = useState(false)
  // Only the latest preview counts, and one approval is in flight at most.
  const previewSeq = useRef(0)
  const approving = useRef(false)
  // The price, and whether it's charged, the consent line last named.
  const shownPrice = useRef("")
  // Ticks every second, so Buy turns off when the review window closes.
  const now = useClock(1000)
  const lapsed =
    !quoted ||
    !isSet(quoted.reviewExpiresAt) ||
    new Date(quoted.reviewExpiresAt).getTime() <= now

  const owned = useMemo(
    () =>
      ownedNames(
        overview.data?.zones ?? [],
        ((registrationList.data ?? overview.data)?.registrations ?? []).filter(
          (r): r is Registration => !!r
        )
      ),
    [overview.data, registrationList.data]
  )
  const ownedLoading = !overview.data && overview.status !== "error"

  const runPreview = async (
    connection: Connection,
    name: string,
    autoRenew: boolean,
    redaction: boolean
  ) => {
    const n = ++previewSeq.current
    setPreviewing(true)
    setError("")
    try {
      const reg = await preview.mutateAsync({
        body: {
          connectionId: connection.id,
          name,
          autoRenew,
          privacyMode: redaction ? "redaction" : "off",
        },
      })
      if (n !== previewSeq.current) return
      // Not in the mock: consent is to a price and a charge, so a new price
      // or a switch out of the sandbox needs it again.
      const terms = `${priceOf(reg.quote)}${reg.sandbox ? " sandbox" : ""}`
      if (terms !== shownPrice.current) setAgree(false)
      shownPrice.current = terms
      setQuoted(reg)
    } catch (err) {
      if (n !== previewSeq.current) return
      shownPrice.current = ""
      setQuoted(undefined)
      setAgree(false)
      // Not in the mock: why there's no price to approve.
      setError(messageOf(err, "Cloudflare gave no price for this name."))
    } finally {
      if (n === previewSeq.current) setPreviewing(false)
    }
  }

  const review = (name: string, connection: Connection) => {
    setQuotedOn(connection)
    setChosen(name)
    shownPrice.current = ""
    setQuoted(undefined)
    setAgree(false)
    setNotice("")
    setStep("review")
    void runPreview(connection, name, renew, privacy)
  }

  const back = () => {
    previewSeq.current++
    shownPrice.current = ""
    setPreviewing(false)
    setQuoted(undefined)
    setAgree(false)
    setError("")
    setNotice("")
    setQuotedOn(undefined)
    setStep("search")
  }

  const flip = (which: "renew" | "privacy") => {
    // What's approved must be what's shown, so nothing changes mid-purchase.
    if (!quotedOn || !stillEligible || approving.current) return
    const nextRenew = which === "renew" ? !renew : renew
    const nextPrivacy = which === "privacy" ? !privacy : privacy
    setRenew(nextRenew)
    setPrivacy(nextPrivacy)
    void runPreview(quotedOn, chosen, nextRenew, nextPrivacy)
  }

  const price = priceOf(quoted?.quote)
  const canBuy =
    agree &&
    stillEligible &&
    !!quoted &&
    !!price &&
    !previewing &&
    !error &&
    !lapsed &&
    !buying

  const buy = async () => {
    const reg = quoted
    if (approving.current || !canBuy || !reg) return
    // The window may have closed since the last tick.
    if (new Date(reg.reviewExpiresAt).getTime() <= Date.now()) return
    approving.current = true
    setBuying(true)
    setNotice("")
    setError("")
    try {
      await approve.mutateAsync({
        path: { registrationId: reg.id },
        body: {
          review: reg.review,
          currency: reg.quote.currency,
          registrationCost: reg.quote.registrationCost,
        },
      })
      invalidate(useDomainsOverview)
      invalidate(useDomainRegistrationsList)
      setStep("pending")
    } catch (err) {
      if (statusOf(err) === 409 && quotedOn) {
        // The quote moved. Review it again; the approval is never retried.
        // Not in the mock.
        setNotice(
          "The price or the review changed before purchase. Nothing was charged. Check the new price and approve it again."
        )
        setAgree(false)
        if (stillEligible) void runPreview(quotedOn, reg.name, renew, privacy)
      } else {
        // Not in the mock.
        setError(messageOf(err, "The registration wasn't approved."))
      }
    } finally {
      approving.current = false
      setBuying(false)
    }
  }

  if (step === "pending") {
    return (
      <>
        <div className="flex flex-col gap-3 px-5 pt-[18px] pb-5">
          <div
            role="status"
            className="flex items-start gap-3 rounded-xl bg-[#E3EEF4] px-3.5 py-3 text-[#1D3B4F] dark:bg-flight-tint dark:text-flight-ink"
          >
            <span
              aria-hidden="true"
              className="mt-[5px] size-2.5 flex-none rounded-full bg-[#2F5C78] shadow-[0_0_0_4px_rgba(47,92,120,0.18)] dark:bg-flight-ink"
            />
            <span className="text-[13px] leading-[1.55]">
              Cloudflare is registering{" "}
              <b className="font-mono font-semibold">{chosen}</b>. You can close
              this. Loomplane keeps checking and notifies you when it&apos;s
              done, then sets up its DNS zone.
            </span>
          </div>
          <span className="text-[12.5px] leading-normal text-muted-foreground">
            If Cloudflare declines the payment, nothing is registered and
            you&apos;ll see why on the Domains page.
          </span>
        </div>
        <div className={cn(footer, "justify-end")}>
          <Button onClick={onClose}>Go to Domains</Button>
        </div>
      </>
    )
  }

  if (step === "review" && quotedOn) {
    const facts: [string, string][] = [
      ["Domain", chosen],
      [
        "Price",
        // Not in the mock: the lines before a price arrives, or without one.
        price
          ? quoted?.sandbox
            ? `${price} for the first year, not charged in the sandbox`
            : `${price} for the first year, charged now`
          : previewing
            ? "Checking the price with Cloudflare…"
            : "No price yet",
      ],
      ["Account", `Cloudflare · ${accountLabel(quotedOn)}`],
      ["Contacts", "The default contact on this Cloudflare account"],
      [
        "After purchase",
        // Not in the mock: a sandbox purchase leaves the real account alone.
        quoted?.sandbox
          ? "Sandbox registrations don't create a DNS zone."
          : "Loomplane creates the DNS zone, so you can attach hostnames to any project.",
      ],
    ]
    const box = "mt-[3px] size-4 flex-none accent-[var(--primary)]"
    const option = "flex items-start gap-2.5 text-[13px] leading-normal"
    return (
      <>
        <div className={body}>
          <dl className="overflow-hidden rounded-xl ring-1 ring-border">
            {facts.map(([label, value]) => (
              <div
                key={label}
                className="flex gap-3 border-t border-border/60 px-3.5 py-[9px] first:border-t-0"
              >
                <dt className="w-[130px] flex-none text-[12.5px] text-muted-foreground">
                  {label}
                </dt>
                <dd className="min-w-0 flex-1 text-[13px] leading-normal break-words">
                  {value}
                </dd>
              </div>
            ))}
          </dl>
          <label className={option}>
            <input
              type="checkbox"
              checked={renew}
              onChange={() => flip("renew")}
              disabled={buying || !stillEligible}
              className={box}
            />
            <span>
              <b className="font-semibold">Renew automatically every year.</b>{" "}
              <span className="text-muted-foreground">
                Loomplane reminds you 30 days before each renewal, with the date
                and the price.
              </span>
            </span>
          </label>
          <label className={option}>
            <input
              type="checkbox"
              checked={privacy}
              onChange={() => flip("privacy")}
              disabled={buying || !stillEligible}
              className={box}
            />
            <span>
              <b className="font-semibold">Hide my contact details in WHOIS</b>{" "}
              <span className="text-muted-foreground">
                where the extension allows it.
              </span>
            </span>
          </label>
          {quoted?.sandbox && price && (
            // Not in the mock: the preview, not the connection, says whether
            // this registrar charges anyone.
            <span className="flex flex-wrap items-center gap-2 text-[12px] leading-normal text-muted-foreground">
              <DomainStatePill tone="warn">Sandbox</DomainStatePill>
              Sandbox registrations are test records. Nothing is charged.
            </span>
          )}
          {quoted && price && (
            <label
              className={cn(option, "rounded-[10px] bg-muted px-3 py-2.5")}
            >
              <input
                type="checkbox"
                checked={agree}
                onChange={() => setAgree((a) => !a)}
                disabled={buying}
                className={box}
              />
              {quoted.sandbox ? (
                // Not in the mock.
                <span>
                  I understand this is a sandbox registration at{" "}
                  <b className="font-semibold">{price}</b>. Nothing is charged.
                </span>
              ) : (
                <span>
                  I understand Cloudflare charges{" "}
                  <b className="font-semibold">{price}</b> to the default
                  payment method on this account now.
                </span>
              )}
            </label>
          )}
          {quoted && lapsed && !previewing && (
            // Not in the mock: the review window closed while this was open.
            <div className="flex flex-wrap items-center justify-between gap-2 text-[12.5px] text-muted-foreground">
              <span>This price is no longer held. Check it again to buy.</span>
              <Button
                variant="outline"
                size="sm"
                disabled={!stillEligible}
                onClick={() =>
                  void runPreview(quotedOn, chosen, renew, privacy)
                }
              >
                Check the price again
              </Button>
            </div>
          )}
          {!stillEligible && (
            // Not in the mock: the account changed under the review.
            <p role="alert" className="text-[12.5px] text-destructive">
              This Cloudflare account can&apos;t register domains any more, so
              this price can&apos;t be approved.
            </p>
          )}
          {(notice || error) && (
            <p role="alert" className="text-[12.5px] text-destructive">
              {notice || error}
            </p>
          )}
        </div>
        <div className={cn(footer, "justify-between")}>
          <Button variant="outline" disabled={buying} onClick={back}>
            Back
          </Button>
          <Button
            disabled={!canBuy}
            onClick={() => void buy()}
            className="min-w-0 truncate"
          >
            {/* Not in the mock: no price to name until the preview answers. */}
            {price ? `Buy ${chosen} for ${price}` : `Buy ${chosen}`}
          </Button>
        </div>
      </>
    )
  }

  return (
    <>
      <div className={body}>
        {connections.status === "error" ? (
          <QueryError
            error={connections.error}
            fallback="Connected accounts couldn't be loaded."
            retry={connections.refetch}
          />
        ) : !connections.data ? (
          <Skeleton className="h-9 rounded-[9px]" />
        ) : !account ? (
          // Not in the mock: no account can register yet.
          <div className="flex flex-col items-start gap-2.5">
            <p className="text-[13px] leading-normal">
              Registration needs a Cloudflare connection with Registrar edit
              permission.
            </p>
            <Button variant="outline" onClick={() => setConnecting(true)}>
              Connect Cloudflare
            </Button>
          </div>
        ) : (
          <>
            <div className="flex flex-col gap-1.5">
              <label className={field}>
                Cloudflare account
                <select
                  value={account.id}
                  onChange={(e) => {
                    setPicked(e.target.value)
                    setChecked({})
                  }}
                  className={cn(control, "h-9 px-2.5 text-[13px] font-normal")}
                >
                  {accounts.map((c) => (
                    <option key={c.id} value={c.id}>
                      {accountLabel(c)}
                    </option>
                  ))}
                </select>
              </label>
              {account.details?.registrar_mode === "sandbox" && (
                // Not in the mock. Only a hint: the review follows the
                // preview's own answer.
                <span className="flex flex-wrap items-center gap-2 text-[12px] leading-normal text-muted-foreground">
                  <DomainStatePill tone="warn">Sandbox</DomainStatePill>
                  Sandbox registrations are test records. Nothing is charged.
                </span>
              )}
            </div>
            <label className={field}>
              Find a name
              <input
                type="search"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className={cn(
                  control,
                  "h-[38px] rounded-[10px] px-3 font-mono text-sm font-normal"
                )}
              />
            </label>
            <Search
              account={account}
              q={q}
              owned={owned}
              ownedLoading={ownedLoading}
              checked={checked}
              onChecked={(offer) =>
                setChecked((c) => ({ ...c, [offer.name]: offer }))
              }
              onAvailable={review}
            />
            <span className="text-xs leading-normal text-muted-foreground">
              Only extensions Cloudflare&apos;s API can register are offered.
              Prices come from Cloudflare and are shown before you buy.
            </span>
          </>
        )}
      </div>
      <div className={cn(footer, "justify-end")}>
        <Button variant="outline" onClick={onClose}>
          Cancel
        </Button>
      </div>
      <ConnectDialog
        open={connecting}
        onOpenChange={setConnecting}
        cloud="cloudflare"
        onConnected={() => setConnecting(false)}
      />
    </>
  )
}

function useDebounced<T>(value: T, delay: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const id = window.setTimeout(() => setSettled(value), delay)
    return () => window.clearTimeout(id)
  }, [value, delay])
  return settled
}

function Search({
  account,
  q,
  ...rest
}: {
  account: Connection
  q: string
  owned: Set<string>
  ownedLoading: boolean
  checked: Record<string, DomainOffer>
  onChecked: (offer: DomainOffer) => void
  /** The name passed its check on this connection. */
  onAvailable: (name: string, connection: Connection) => void
}) {
  const term = useDebounced(q.trim().toLowerCase(), 300)
  if (!term) return null
  return <Results key={account.id} account={account} term={term} {...rest} />
}

function Results({
  account,
  term,
  owned,
  ownedLoading,
  checked,
  onChecked,
  onAvailable,
}: {
  account: Connection
  term: string
  owned: Set<string>
  ownedLoading: boolean
  checked: Record<string, DomainOffer>
  onChecked: (offer: DomainOffer) => void
  /** The name passed its check on this connection. */
  onAvailable: (name: string, connection: Connection) => void
}) {
  const extensions = useQuery(useDomainRegistrationExtensions, {
    query: { connection_id: account.id },
  })
  const search = useQuery(useDomainRegistrationSearch, {
    query: { connection_id: account.id, q: term },
  })
  const check = useMutation(useDomainRegistrationCheck)
  const [checking, setChecking] = useState("")
  const [error, setError] = useState("")
  const tlds = extensions.data?.extensions ?? []

  const select = async (name: string) => {
    if (checking) return
    setChecking(name)
    setError("")
    try {
      const res = (await check.mutateAsync({
        body: { connectionId: account.id, domains: [name] },
      })) as DomainOfferList
      const offer = res.offers.find((o) => o.name === name)
      if (!offer) {
        // Not in the mock.
        setError(`Cloudflare didn't answer for ${name}. Try again.`)
        return
      }
      onChecked(offer)
      // A name that's gone since the search stays here, its pill updated.
      if (offerStatus(offer, owned, tlds) === "available" && offer.price)
        onAvailable(name, account)
    } catch (err) {
      // Not in the mock.
      setError(messageOf(err, `Cloudflare couldn't check ${name}.`))
    } finally {
      setChecking("")
    }
  }

  const failed =
    search.status === "error"
      ? search
      : extensions.status === "error"
        ? extensions
        : undefined
  if (failed)
    return (
      <QueryError
        error={failed.error}
        // Not in the mock.
        fallback="Cloudflare couldn't search for names."
        retry={failed.refetch}
      />
    )

  const box = "overflow-hidden rounded-xl ring-1 ring-border"
  if (!search.data || !extensions.data || ownedLoading)
    return (
      <div className={cn(box, "flex flex-col gap-2 p-3.5")}>
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-5 w-1/2" />
        <Skeleton className="h-5 w-3/5" />
      </div>
    )

  const offers = search.data.offers.map((o) => checked[o.name] ?? o)
  return (
    <>
      {offers.length === 0 ? (
        // Not in the mock.
        <p className="text-[12.5px] text-muted-foreground">
          Cloudflare found no names for {term}.
        </p>
      ) : (
        <ul aria-label="Names" className={box}>
          {offers.map((o) => {
            const s = offerStatus(o, owned, tlds)
            const price = s === "available" ? priceOf(o.price) : ""
            return (
              <li
                key={o.name}
                aria-label={o.name}
                className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-border/60 px-3.5 py-2.5 first:border-t-0"
              >
                <span className="min-w-0 flex-[1_1_200px] font-mono text-[13.5px] font-medium break-all">
                  {o.name}
                </span>
                <DomainStatePill tone={offerTone(s)}>
                  {offerLabel(s)}
                </DomainStatePill>
                <span className="min-w-[110px] text-right text-[12.5px] text-foreground/80">
                  {price && `${price} / year`}
                </span>
                {price && (
                  <Button
                    size="sm"
                    className="px-3 text-[12.5px]"
                    disabled={!!checking}
                    onClick={() => void select(o.name)}
                  >
                    Select
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {error && (
        <p role="alert" className="text-[12.5px] text-destructive">
          {error}
        </p>
      )}
    </>
  )
}
