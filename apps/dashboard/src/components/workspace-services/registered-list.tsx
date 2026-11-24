// The workspace services people registered by their connection strings,
// as the Catalog lists them, with the way to register another.
import { ChevronRightIcon, DatabaseIcon, PlusIcon } from "lucide-react"
import { useState } from "react"

import { useCatalogEndpointsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  engineLogos,
  RegisterServiceDialog,
} from "@/components/workspace-services/register-dialog"
import { engineTitles, type ConnectionEngine } from "@/lib/connection-string"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

/** RegisterButton opens the register dialog, for those who may register. */
export function RegisterButton({
  variant = "outline",
}: {
  variant?: "outline" | "default"
}) {
  const { can } = useOrganization()
  const [open, setOpen] = useState(false)
  if (!can("secret:manage")) return null
  return (
    <>
      <Button variant={variant} onClick={() => setOpen(true)}>
        <PlusIcon />
        Register a service
      </Button>
      <RegisterServiceDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

/**
 * RegisteredServices lists the workspace services registered by their
 * connection strings. A provider resource's own endpoints are listed
 * with their resource instead.
 */
export function RegisteredServices() {
  const list = useQuery(useCatalogEndpointsList)
  const registered = (list.data?.endpoints ?? []).flatMap((e) =>
    e && (e.origin === "" || e.origin === "registered") ? [e] : []
  )
  return (
    <div className="grid gap-2">
      <div className="flex items-baseline justify-between">
        <h2 className="text-[13px] font-medium text-muted-foreground">
          Registered by connection string
        </h2>
        <span className="text-[12.5px] text-muted-foreground">
          {registered.length ? `${registered.length} registered` : "None yet"}
        </span>
      </div>
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="Workspace services couldn't be loaded."
          retry={list.refetch}
        />
      ) : !list.data ? (
        <Skeleton className="h-16 rounded-xl" />
      ) : registered.length === 0 ? (
        <p className="rounded-xl px-4 py-3 text-[12.5px] text-muted-foreground ring-1 ring-border">
          Already run a database or cache somewhere else? Register it by its
          connection string, and your projects can use it like one Loomplane
          runs.
        </p>
      ) : (
        <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
          {registered.map((e) => {
            const check = e.lastCheck
            const title = engineTitles[e.engine as ConnectionEngine] ?? e.engine
            return (
              <Link
                key={e.id}
                to="/services/registered/$id"
                params={{ id: e.id }}
                className="group flex items-center gap-3 px-4 py-3 text-foreground transition-colors hover:bg-subtle"
              >
                <ProductIcon
                  product={engineLogos[e.engine]}
                  fallback={DatabaseIcon}
                  label={title}
                  size="md"
                />
                <span className="grid min-w-0 flex-1 gap-0.5">
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-mono text-sm font-semibold">
                      {e.name}
                    </span>
                    {e.environmentLabel && (
                      <span className="rounded-full bg-muted px-2 text-[11.5px] text-muted-foreground">
                        {e.environmentLabel}
                      </span>
                    )}
                  </span>
                  <span className="truncate text-[12.5px] text-muted-foreground">
                    {title} · {e.host}:{e.port}
                    {" · "}
                    {e.restricted
                      ? `${e.projects.length === 1 ? "1 project" : `${e.projects.length} projects`}`
                      : "Every project"}
                  </span>
                </span>
                {check && !check.ok && (
                  <StatusPill tone="failed">Check failed</StatusPill>
                )}
                <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}
