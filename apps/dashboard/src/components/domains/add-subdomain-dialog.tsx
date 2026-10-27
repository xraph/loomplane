// Adding a zone under a domain from its page. It's verified through the
// domain, so there's no TXT record to add, and its DNS stays with the
// domain's unless you say it's hosted elsewhere. Not in the mock.
import { useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDnsZonesCreate,
  useDnsZonesGet,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type { Zone } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { cloudLabel } from "@/lib/clouds"
import { subdomainName, validSubdomainLabel } from "@/lib/domains"

// The providers Loomplane writes route records with.
const hosts = ["cloudflare", "digitalocean", "vercel"]

export function AddSubdomainDialog({
  domain,
  open,
  onOpenChange,
  onAdded,
}: {
  domain: Zone
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: (zone: Zone) => void
}) {
  const fieldID = useId()
  const switchLabel = useId()
  const connections = useQuery(useConnectionsList)
  const create = useMutation(useDnsZonesCreate)
  const invalidate = useInvalidate()
  const [label, setLabel] = useState("")
  const [elsewhere, setElsewhere] = useState(false)
  const [picked, setPicked] = useState("")
  const [error, setError] = useState("")
  const name = subdomainName(label, domain.name)
  const ready = (connections.data?.connections ?? []).filter(
    (c) => hosts.includes(c.cloud) && c.status === "ready"
  )

  // Closing starts the next one afresh.
  const show = (next: boolean) => {
    if (!next) {
      setLabel("")
      setElsewhere(false)
      setPicked("")
      setError("")
    }
    onOpenChange(next)
  }

  const add = async () => {
    setError("")
    if (!validSubdomainLabel(label)) {
      setError("Enter a label like app or api.eu.")
      return
    }
    const connection = elsewhere
      ? ready.find((c) => c.id === picked)
      : undefined
    const managed = elsewhere || domain.mode === "managed"
    try {
      const zone = await create.mutateAsync({
        body: {
          name,
          mode: managed ? "managed" : "external",
          connectionId: elsewhere
            ? picked
            : managed
              ? domain.connectionId
              : undefined,
          // Granted to projects afterwards, from its own page.
          projectIds: [],
        },
      })
      invalidate(useDomainsOverview)
      invalidate(useDnsZonesGet)
      show(false)
      onAdded(zone)
    } catch (err) {
      const provider = connection?.cloud ?? domain.provider ?? ""
      setError(
        managed && statusOf(err) === 404
          ? `${name} isn't a zone in that ${cloudLabel(provider)} account.`
          : messageOf(err, "The subdomain wasn't added.")
      )
    }
  }

  return (
    <Dialog open={open} onOpenChange={show}>
      <DialogContent className="rounded-2xl sm:max-w-[520px]">
        <DialogHeader className="gap-1">
          <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
            Add a subdomain
          </DialogTitle>
          <DialogDescription className="leading-normal">
            It&apos;s verified through {domain.name}, so you don&apos;t add a
            TXT record for it. Projects need their own grant to use it.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void add()
          }}
        >
          <label
            htmlFor={fieldID}
            className="flex flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80"
          >
            Subdomain
            <input
              id={fieldID}
              type="text"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="app"
              autoComplete="off"
              spellCheck={false}
              className="h-9 rounded-[9px] border border-input bg-background px-2.5 font-mono text-[13px] font-normal text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
            />
          </label>
          <p
            aria-live="polite"
            className="-mt-1 font-mono text-[12.5px] break-all text-muted-foreground"
          >
            {label.trim() ? name : `app.${domain.name}`}
          </p>
          <div className="flex items-start gap-2.5">
            <Switch
              aria-labelledby={switchLabel}
              checked={elsewhere}
              onCheckedChange={(on) => {
                setElsewhere(on)
                setError("")
              }}
              className="mt-0.5"
            />
            <div className="grid gap-0.5">
              <span id={switchLabel} className="text-[13px] font-medium">
                Hosted elsewhere
              </span>
              <span className="text-[12.5px] leading-normal text-muted-foreground">
                Its DNS sits in another connected account, and Loomplane writes
                its records there.
              </span>
            </div>
          </div>
          {elsewhere &&
            (ready.length > 0 ? (
              <div
                role="radiogroup"
                aria-label="Connected account"
                className="flex flex-col gap-1.5"
              >
                {ready.map((c) => (
                  <label
                    key={c.id}
                    className="flex items-center gap-2 rounded-[9px] px-2.5 py-2 text-[13px] ring-1 ring-border has-checked:ring-ring"
                  >
                    <input
                      type="radio"
                      name={`${fieldID}-connection`}
                      value={c.id}
                      checked={picked === c.id}
                      onChange={() => setPicked(c.id)}
                    />
                    <span className="font-medium">{c.name}</span>
                    <span className="text-muted-foreground">
                      {cloudLabel(c.cloud)}
                    </span>
                    {c.account && (
                      <span className="ml-auto truncate font-mono text-[11px] text-muted-foreground">
                        {c.account}
                      </span>
                    )}
                  </label>
                ))}
              </div>
            ) : (
              <p className="text-[12.5px] leading-normal text-muted-foreground">
                {connections.status === "error"
                  ? messageOf(
                      connections.error,
                      "Connected accounts couldn't be loaded."
                    )
                  : connections.data
                    ? "You have no Cloudflare, DigitalOcean or Vercel account connected and ready. Connect one from Clouds first."
                    : "Loading your connected accounts."}
              </p>
            ))}
          {error && (
            <p role="alert" className="text-[12.5px] text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              type="submit"
              disabled={create.isPending || (elsewhere && !picked)}
            >
              {create.isPending && <Spinner />}
              Add subdomain
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
