// Removing a domain, or one of its subdomains, from Loomplane. The zones
// under it go with it, and hostnames still pointing at any of them have to
// be removed first. You type the name to confirm. Not in the mock.
import { useId, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useDnsZonesGet,
  useDnsZonesRemove,
  useDomainsOverview,
} from "@/client/forge/loomplane/hooks"
import type { DomainDetail, ZoneOverview } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Spinner } from "@/components/ui/spinner"
import {
  type BlockingHostname,
  cloudflareRegistrations,
  hostnameInZone,
  zoneInUse,
} from "@/lib/domains"
import { Link } from "@/lib/router"

export function RemoveDomainDialog({
  detail,
  zone,
  open,
  onOpenChange,
  onRemoved,
}: {
  detail: DomainDetail
  /** One of the page's subdomains. Without it, the page's own zone goes. */
  zone?: ZoneOverview
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Awaited before the page reads anything again. */
  onRemoved: () => void | Promise<void>
}) {
  const fieldID = useId()
  const remove = useMutation(useDnsZonesRemove)
  const invalidate = useInvalidate()
  const [typed, setTyped] = useState("")
  const [refused, setRefused] = useState<BlockingHostname[] | null>(null)
  const [error, setError] = useState("")

  const target = zone ?? detail.domain
  const z = target.zone
  const name = z?.name ?? ""
  // Every zone under it, at any depth, is removed with it.
  const under = zone
    ? detail.subdomains.filter(
        (s) =>
          !!s.zone && s.zone.name !== name && hostnameInZone(s.zone.name, name)
      )
    : detail.subdomains
  const known = [target, ...under].flatMap((o) =>
    o.hostnames.map((h): BlockingHostname => ({
      routeId: h.routeId,
      hostname: h.hostname,
      zoneId: o.zone?.id ?? "",
      zoneName: o.zone?.name ?? "",
      projectId: h.projectId,
      projectName: h.projectName,
    }))
  )
  // What the API refused with wins: it knows hostnames the page doesn't yet.
  const blocking = refused ?? known
  const kind = zone || detail.domain.parentZoneId ? "subdomain" : "domain"
  // A registration belongs to the domain, not to a subdomain on its page.
  const registration = zone ? undefined : detail.registration

  const show = (next: boolean) => {
    if (!next) {
      setTyped("")
      setRefused(null)
      setError("")
    }
    onOpenChange(next)
  }

  const confirm = async () => {
    if (!z || remove.isPending) return
    setError("")
    try {
      await remove.mutateAsync({
        path: { zoneId: z.id },
        body: { revision: z.revision },
      })
    } catch (err) {
      const inUse = zoneInUse(err)
      if (inUse && inUse.length > 0) {
        setRefused(inUse)
        // The page's own hostnames catch up with what the API knows.
        invalidate(useDnsZonesGet)
        return
      }
      setError(messageOf(err, `The ${kind} wasn't removed.`))
      // Any other failure is read again, the list included: a stale
      // revision needs the new one, and a removal that stopped part way
      // has already taken some subdomains with it.
      invalidate(useDnsZonesGet)
      invalidate(useDomainsOverview)
      return
    }
    toast.success(`Removed ${name}`)
    show(false)
    // Leaving a removed zone's own page first keeps it from reading the
    // zone again and showing not found. The zone is gone even if leaving
    // fails, so that isn't a failed removal, and the page still reads again.
    try {
      await onRemoved()
    } catch {
      // Reading again below shows where things stand.
    } finally {
      invalidate(useDomainsOverview)
      invalidate(useDnsZonesGet)
    }
  }

  const ready = !!z && typed === name && blocking.length === 0

  return (
    <AlertDialog open={open} onOpenChange={show}>
      <AlertDialogContent className="data-[size=default]:sm:max-w-[480px]">
        <AlertDialogHeader>
          <AlertDialogTitle className="break-all">
            Remove {name}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane deletes only the DNS records it wrote there, such as the
            TXT proof. Other records stay as they are.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="grid min-w-0 gap-3 text-[13px] leading-normal">
          {under.length > 0 && (
            <div className="grid gap-1">
              <p>
                {under.length === 1
                  ? "This subdomain goes with it:"
                  : "These subdomains go with it:"}
              </p>
              <ul
                aria-label="Subdomains removed with it"
                className="grid gap-0.5 font-mono text-[12.5px] break-all text-foreground/80"
              >
                {under.map((s) => (
                  <li key={s.zone?.id}>{s.zone?.name}</li>
                ))}
              </ul>
            </div>
          )}
          {blocking.length > 0 && (
            <div className="grid gap-1">
              <p className="font-medium text-destructive">
                Remove these hostnames first.
              </p>
              <ul className="grid gap-1 text-[12.5px]">
                {blocking.map((h, i) => (
                  <li key={h.routeId || h.hostname || i} className="min-w-0">
                    {h.projectId ? (
                      <Link
                        to="/projects/$id"
                        params={{ id: h.projectId }}
                        className="font-mono font-medium break-all text-foreground underline-offset-4 hover:underline"
                      >
                        {h.hostname}
                      </Link>
                    ) : (
                      <span className="font-mono font-medium break-all text-foreground">
                        {h.hostname}
                      </span>
                    )}
                    {(h.projectName || h.projectId) && (
                      <>
                        {" "}
                        <span className="text-muted-foreground">
                          in {h.projectName || h.projectId}
                        </span>
                      </>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {registration && (
            <p>
              The registration stays in your Cloudflare account and auto-renew
              is unchanged.{" "}
              <a
                href={cloudflareRegistrations(registration.accountId)}
                target="_blank"
                rel="noreferrer"
                className="font-medium underline underline-offset-4"
              >
                Open in Cloudflare
              </a>
            </p>
          )}
          <label
            htmlFor={fieldID}
            className="flex flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80"
          >
            <span>
              Type <span className="font-mono break-all">{name}</span> to
              confirm
            </span>
            <input
              id={fieldID}
              type="text"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="h-9 rounded-[9px] border border-input bg-background px-2.5 font-mono text-[13px] font-normal text-foreground outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
            />
          </label>
          {error && (
            <p role="alert" className="text-[12.5px] text-destructive">
              {error}
            </p>
          )}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep {kind}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={!ready || remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Remove {kind}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
