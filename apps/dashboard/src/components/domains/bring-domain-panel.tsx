// Adding a domain the organization already owns: either a connected
// account hosts its DNS and Loomplane manages the records it creates, or
// the DNS stays put and a TXT record proves the domain is yours.
import { useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDnsZonesCreate,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Zone,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { ConnectDialog } from "@/components/cloud-connections"
import { ProductMark } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { Button } from "@/components/ui/button"
import { cloudLabel } from "@/lib/clouds"
import { parentZoneOf } from "@/lib/domains"

const managed: { cloud: string; product: ProductKey }[] = [
  { cloud: "cloudflare", product: "cloudflare" },
  { cloud: "digitalocean", product: "digitalocean" },
  { cloud: "vercel", product: "vercel" },
]

export function BringDomainPanel({
  zones = [],
  onAdded,
}: {
  /** The organization's zones, to tell a subdomain from a domain. */
  zones?: ZoneOverview[]
  onAdded: (zone: Zone) => void
}) {
  const fieldID = useId()
  const noteID = useId()
  const connections = useQuery(useConnectionsList)
  const create = useMutation(useDnsZonesCreate)
  const [name, setName] = useState("")
  // The provider whose accounts are listed to pick from.
  const [listing, setListing] = useState("")
  const [connecting, setConnecting] = useState("")
  const [error, setError] = useState("")
  const domain = name.trim().toLowerCase()
  const ancestor = parentZoneOf(domain, zones)
  const parent = ancestor?.zone?.name
  // The zone whose TXT record verifies the ancestor verifies this one too.
  const proving =
    zones.find((z) => z.zone && z.zone.id === ancestor?.proofZoneId)?.zone
      ?.name ?? parent
  const ready = (cloud: string) =>
    (connections.data?.connections ?? []).filter(
      (c) => c.cloud === cloud && c.status === "ready"
    )

  const add = async (connection?: Connection) => {
    setError("")
    if (!domain) {
      setError("Enter the domain you own.")
      return
    }
    try {
      const zone = await create.mutateAsync({
        body: {
          name: domain,
          mode: connection ? "managed" : "external",
          connectionId: connection?.id,
          // Granted to projects afterwards, from the domain's page.
          projectIds: [],
        },
      })
      setListing("")
      onAdded(zone)
    } catch (err) {
      setError(
        connection && statusOf(err) === 404
          ? `${domain} isn't a zone in that ${cloudLabel(connection.cloud)} account.`
          : messageOf(err, "The domain wasn't added.")
      )
    }
  }

  const pick = (cloud: string) => {
    setError("")
    if (!domain) {
      setError("Enter the domain you own.")
      return
    }
    const accounts = ready(cloud)
    if (accounts.length === 1) {
      void add(accounts[0])
    } else if (accounts.length === 0) {
      setListing("")
      setConnecting(cloud)
    } else {
      setListing(cloud)
    }
  }

  const option = "flex flex-col gap-2 rounded-xl bg-card p-3 ring-1 ring-border"
  return (
    <section
      aria-label="Bring a domain"
      className="flex flex-col gap-3 rounded-[14px] bg-subtle px-4 py-3.5 ring-1 ring-border"
    >
      <label
        htmlFor={fieldID}
        className="flex max-w-[360px] flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80"
      >
        Domain you own
        <input
          id={fieldID}
          aria-describedby={parent ? noteID : undefined}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="example.com"
          autoComplete="off"
          spellCheck={false}
          className="h-9 rounded-[9px] border border-input bg-background px-2.5 font-mono text-[13px] font-normal text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
        />
      </label>
      {parent && (
        // Not in the mock: the redesign's sentence for a subdomain.
        <p
          id={noteID}
          className="-mt-1 text-[12.5px] leading-normal text-muted-foreground"
        >
          {domain} is a subdomain of {parent}, so it uses {proving}&apos;s
          verification.
        </p>
      )}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-2">
        <div className={option}>
          <span className="text-[13px] font-semibold">
            Loomplane manages its DNS
          </span>
          <span className="text-[12.5px] leading-normal text-muted-foreground">
            Pick the connected account that hosts the zone. Loomplane only
            changes records it created.
          </span>
          <div className="flex flex-wrap gap-1.5">
            {managed.map((m) => (
              <Button
                key={m.cloud}
                variant="outline"
                size="sm"
                aria-pressed={listing === m.cloud}
                disabled={!connections.data || create.isPending}
                onClick={() => pick(m.cloud)}
              >
                <ProductMark product={m.product} size={13} />
                {cloudLabel(m.cloud)}
              </Button>
            ))}
          </div>
          {listing && (
            // Not in the mock: with several accounts, which one hosts it.
            <div
              role="group"
              aria-label={`${cloudLabel(listing)} accounts`}
              className="flex flex-wrap gap-1.5"
            >
              {ready(listing).map((c) => (
                <Button
                  key={c.id}
                  variant="outline"
                  size="sm"
                  disabled={create.isPending}
                  onClick={() => void add(c)}
                >
                  {c.name}
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {c.account}
                  </span>
                </Button>
              ))}
            </div>
          )}
        </div>
        <div className={option}>
          <span className="text-[13px] font-semibold">
            Keep DNS where it is
          </span>
          <span className="text-[12.5px] leading-normal text-muted-foreground">
            Prove you own it with a TXT record. You add each hostname&apos;s
            record yourself, and Loomplane checks it.
          </span>
          <Button
            variant="outline"
            size="sm"
            className="self-start"
            disabled={create.isPending}
            onClick={() => void add()}
          >
            Verify with TXT
          </Button>
        </div>
      </div>
      {(error || connections.status === "error") && (
        <p role="alert" className="text-[12.5px] text-destructive">
          {error ||
            messageOf(
              connections.error,
              "Connected accounts couldn't be loaded."
            )}
        </p>
      )}
      <ConnectDialog
        open={!!connecting}
        onOpenChange={(open) => {
          if (!open) setConnecting("")
        }}
        cloud={connecting}
        onConnected={(c) => {
          setConnecting("")
          if (c.status === "ready") void add(c)
        }}
      />
    </section>
  )
}
