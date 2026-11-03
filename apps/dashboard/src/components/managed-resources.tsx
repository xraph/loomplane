// The provider-managed half of "Yours" on the Services page: resources a
// connected cloud runs that your deployments can use. Each row says which
// provider manages it; the full inventory stays under Integrations.
import { ChevronRightIcon } from "lucide-react"
import { useState } from "react"

import { useProviderResourcesList } from "@/client/forge/loomplane/hooks"
import type { Connection, Provider } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ResourceDetails } from "@/components/provider-resources/resource-details"
import { ResourceReviewDialog } from "@/components/provider-resources/review-dialog"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { useOrganization } from "@/lib/organization"
import {
  providerResourceReadiness,
  usableInDeployments,
  type InventoryResource,
} from "@/lib/provider-resources"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

export function ManagedResources({
  providers,
  connections,
}: {
  providers: Provider[]
  connections: Connection[]
}) {
  const { can } = useOrganization()
  const inventory = useQuery(useProviderResourcesList)
  const now = useNow()
  const [selected, setSelected] = useState<InventoryResource>()
  const [attaching, setAttaching] = useState<InventoryResource>()
  const resources = (inventory.data?.resources ?? [])
    .filter((r) => r !== null)
    .filter((r) => usableInDeployments(r, now))
  // Name a kind the way its provider's offering does, such as "Cloudflare
  // D1 database" for cloudflare-d1.
  const creates = new Map(
    providers.flatMap((p) =>
      (p.provisions ?? []).map((o) => [o.kind, o.creates])
    )
  )

  return (
    <div className="grid gap-2">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-[13px] font-medium text-muted-foreground">
          Managed by a provider
        </h2>
        <Link
          to="/integrations/resources"
          className="text-[12.5px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          All provider resources
        </Link>
      </div>
      {inventory.status === "error" ? (
        <QueryError
          error={inventory.error}
          fallback="Provider resources couldn't be loaded."
          retry={inventory.refetch}
        />
      ) : !inventory.data ? (
        <Skeleton className="h-14 rounded-xl" />
      ) : resources.length === 0 ? (
        <p className="rounded-xl px-4 py-3 text-[12.5px] text-muted-foreground ring-1 ring-border">
          None found in your connected accounts yet. Create one in one click
          from the{" "}
          <Link
            to="/services/catalog"
            search={{ group: "managed" }}
            className="text-foreground underline underline-offset-4"
          >
            catalog
          </Link>
          , or sync your accounts in Integrations.
        </p>
      ) : (
        <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
          {resources.map((r) => {
            const readiness = providerResourceReadiness(r, now)
            const used = (r.consumers ?? []).length
            return (
              <button
                key={r.id}
                type="button"
                onClick={() => setSelected(r)}
                className="group flex w-full items-center gap-3 px-4 py-3 text-left text-foreground transition-colors hover:bg-subtle"
              >
                <CloudGlyph cloud={r.provider} size="md" />
                <span className="grid min-w-0 flex-1 gap-0.5">
                  <span className="truncate text-sm font-semibold">
                    {r.name}
                  </span>
                  <span className="truncate text-[12.5px] text-muted-foreground">
                    {creates.get(r.kind) ?? r.kind}
                    {r.region && ` · ${r.region}`}
                    {used > 0 &&
                      ` · ${used} ${used === 1 ? "deployment" : "deployments"}`}
                  </span>
                </span>
                <StatusPill tone="quiet" className="hidden sm:inline-flex">
                  Managed by {cloudLabel(r.provider)}
                </StatusPill>
                {readiness.credentials !== "Credentials saved" && (
                  <StatusPill tone="waiting">
                    {readiness.credentials}
                  </StatusPill>
                )}
                <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
              </button>
            )
          })}
        </div>
      )}
      {selected && (
        <ResourceDetails
          id={selected.id}
          open
          onOpenChange={(open) => {
            if (!open) setSelected(undefined)
          }}
          canConnect={can("secret:manage")}
          canReview={can("project:update") && can("deployment:create")}
          onAttach={(resource) => {
            setSelected(undefined)
            setAttaching(resource)
          }}
          onSaved={() => inventory.refetch()}
        />
      )}
      {attaching && (
        <ResourceReviewDialog
          open
          onOpenChange={(open) => {
            if (!open) setAttaching(undefined)
          }}
          resource={attaching}
          providers={providers}
          connections={connections}
        />
      )}
    </div>
  )
}
