// Provider resources: what Loomplane found in your connected cloud
// accounts. Each one is run by its provider, so this lives with the
// integrations that reach it. New ones are created from the Services
// catalog, next to the services Loomplane runs itself.
import { DatabaseIcon, RefreshCwIcon } from "lucide-react"
import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { ResourceInventory } from "@/components/provider-resources/inventory"
import { ResourceDetails } from "@/components/provider-resources/resource-details"
import { ResourceReviewDialog } from "@/components/provider-resources/review-dialog"
import { ResourceSyncDialog } from "@/components/provider-resources/sync-dialog"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateMedia,
} from "@/components/zero-state"
import { useOrganization } from "@/lib/organization"
import type { InventoryResource } from "@/lib/provider-resources"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

export function ProviderResources({
  connection = "",
}: {
  connection?: string
}) {
  const { session, can } = useOrganization()
  if (!can("infrastructure:read"))
    return (
      <ZeroState variant="compact" preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <DatabaseIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>Resource access is restricted</ZeroStateTitle>
          <ZeroStateDescription>
            Ask your workspace owner for permission to view provider resources.
          </ZeroStateDescription>
        </ZeroStateHeader>
      </ZeroState>
    )
  return <ResourceWorkspace key={session.orgId} connection={connection} />
}
function ResourceWorkspace({ connection }: { connection: string }) {
  const { can } = useOrganization()
  const inventory = useQuery(api.useProviderResourcesList)
  const refetchInventory = inventory.refetch
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible")
        void refetchInventory().catch(() => undefined)
    }, 60_000)
    return () => window.clearInterval(timer)
  }, [refetchInventory])
  const accounts = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const sync = useMutation(api.useProviderResourcesSync)
  const now = useNow()
  const [syncing, setSyncing] = useState(false)
  const [selected, setSelected] = useState<InventoryResource>()
  const [attaching, setAttaching] = useState<InventoryResource>()
  const resources = (inventory.data?.resources ?? []).filter((r) => r !== null)
  const syncs = (inventory.data?.syncs ?? []).filter((s) => s !== null)
  const latest = new Map<string, (typeof syncs)[number]>()
  for (const attempt of syncs)
    if (
      !latest.has(attempt.connectionId) ||
      attempt.startedAt > latest.get(attempt.connectionId)!.startedAt
    )
      latest.set(attempt.connectionId, attempt)
  const failed = [...latest.values()].filter(
    (s) => !["complete", "running"].includes(s.status)
  )
  const supported = inventory.data?.supportedProviders ?? []
  const syncAccounts = (accounts.data?.connections ?? []).filter((c) =>
    supported.includes(c.cloud)
  )
  const canReview = can("project:update") && can("deployment:create")
  const usage = Object.fromEntries(
    resources.map((resource) => [
      resource.id,
      (resource.consumers ?? []).length,
    ])
  )
  const performSync = async (connectionId: string) => {
    try {
      const result = await sync.mutateAsync({ path: { connectionId } })
      if (!["complete", "running"].includes(result.status))
        throw new Error(
          result.error ||
            "Sync did not complete. Your previous inventory has been kept."
        )
    } finally {
      await inventory.refetch().catch(() => undefined)
    }
  }
  return (
    <>
      <div className="grid gap-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <p className="max-w-2xl text-[13px] leading-relaxed text-muted-foreground">
            {resources.length} provider{" "}
            {resources.length === 1 ? "resource" : "resources"} found in your
            connected accounts. Each one is run by its provider, not by
            Loomplane. To create one, pick it from the{" "}
            <Link
              to="/services/catalog"
              search={{ group: "managed" }}
              className="text-foreground underline underline-offset-4"
            >
              catalog
            </Link>
            .
          </p>
          <Button
            size="sm"
            variant="outline"
            disabled={
              !can("cloud_account:manage") || inventory.status === "error"
            }
            onClick={() => setSyncing(true)}
          >
            <RefreshCwIcon />
            Sync accounts
          </Button>
        </div>
        {inventory.status === "error" ? (
          <QueryError
            error={inventory.error}
            fallback="Resource inventory could not be loaded."
            retry={inventory.refetch}
          />
        ) : !inventory.data ? (
          <div aria-label="Loading resources" className="grid gap-2">
            {[1, 2, 3].map((n) => (
              <Skeleton key={n} className="h-12" />
            ))}
          </div>
        ) : (
          <>
            {failed.length > 0 && (
              <Alert>
                <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
                  <span>
                    {failed.length}{" "}
                    {failed.length === 1 ? "account needs" : "accounts need"}{" "}
                    attention.{" "}
                    {failed[0].error ||
                      "Discovery did not complete. Previous observations are kept."}
                  </span>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setSyncing(true)}
                  >
                    Review syncs
                  </Button>
                </AlertDescription>
              </Alert>
            )}
            {accounts.status === "error" && (
              <QueryError
                error={accounts.error}
                fallback="Connected accounts could not be loaded."
                retry={accounts.refetch}
              />
            )}
            {providers.status === "error" && (
              <QueryError
                error={providers.error}
                fallback="Provider capabilities could not be loaded."
                retry={providers.refetch}
              />
            )}
            <ResourceInventory
              resources={resources}
              connections={accounts.data?.connections ?? []}
              connection={connection}
              now={now}
              onSelect={setSelected}
              onSync={() => setSyncing(true)}
              canSync={can("cloud_account:manage")}
              usage={usage}
              usageLoaded
            />
          </>
        )}
      </div>
      {syncing && (
        <ResourceSyncDialog
          accounts={syncAccounts}
          syncs={syncs}
          open
          onOpenChange={setSyncing}
          onSync={performSync}
          canSync={can("cloud_account:manage")}
        />
      )}
      {selected && (
        <ResourceDetails
          id={selected.id}
          open
          onOpenChange={(open) => {
            if (!open) setSelected(undefined)
          }}
          canConnect={can("secret:manage")}
          canReview={canReview}
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
          providers={providers.data?.providers ?? []}
          connections={accounts.data?.connections ?? []}
        />
      )}
    </>
  )
}
