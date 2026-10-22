import { useState } from "react"
import { CloudIcon } from "lucide-react"

import { useMutation, useQuery } from "@/client/hooks"
import {
  useNativeRecoverySourcesList,
  useNativeRecoverySourcesInventory,
  useNativeRecoverySourcesRefresh,
} from "@/client/forge/loomplane/hooks"
import type { NativeSource } from "@/client/forge/loomplane/types"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { QueryError } from "@/components/query-error"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatDate } from "@/lib/format"

function NativeEmpty({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <ZeroState variant="inline" className="p-3">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <CloudIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

export function NativeRecovery({
  project,
  environment,
}: {
  project: string
  environment?: string
}) {
  const [pages, setPages] = useState<string[]>([""])
  const [chosen, setChosen] = useState("")
  const sources = useQuery(useNativeRecoverySourcesList, {
    path: { id: project },
    query: {
      before: pages.at(-1) || undefined,
      limit: 50,
      environment_id: environment,
    },
  })
  const visible = (sources.data?.sources ?? []).filter(
    (source) => source.environmentId === environment
  )
  const selected = visible.find((source) => source.id === chosen) ?? visible[0]
  return (
    <section aria-label="Cloud snapshots" className="grid min-w-0 gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium">Cloud snapshots</h3>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => void sources.refetch().catch(() => undefined)}
        >
          Reload histories
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Saved provider observations, separate from Trove exports. A snapshot
        listed here has not been restore-tested. Isolated native restore and
        PITR controls remain unavailable.
      </p>
      {sources.status === "error" && (
        <QueryError
          error={sources.error}
          fallback="Could not load cloud histories"
          retry={sources.refetch}
        />
      )}
      {sources.status === "pending" && !sources.data ? (
        <p role="status">Loading cloud histories…</p>
      ) : selected ? (
        <>
          <Select
            value={selected.id}
            onValueChange={(value) => setChosen(value ?? "")}
          >
            <SelectTrigger
              aria-label="Cloud database history"
              className="w-full sm:max-w-lg"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {visible.map((source) => (
                <SelectItem key={source.id} value={source.id}>
                  {source.name} · {source.region} · {source.physicalId}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <NativePoints
            key={selected.id}
            source={selected}
            reloadSources={sources.refetch}
          />
        </>
      ) : sources.status !== "error" ? (
        <NativeEmpty title="No captured cloud database history">
          Pinned RDS histories appear after deployment. Older unpinned databases
          and Cloud SQL inventory still need qualification. You can deploy a
          managed database in Services.
        </NativeEmpty>
      ) : null}
      {(pages.length > 1 || sources.data?.nextBefore) && (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={pages.length === 1}
            onClick={() => setPages((current) => current.slice(0, -1))}
          >
            Previous histories
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!sources.data?.nextBefore || sources.status !== "success"}
            onClick={() =>
              setPages((current) => [...current, sources.data!.nextBefore!])
            }
          >
            More histories
          </Button>
        </div>
      )}
    </section>
  )
}

function NativePoints({
  source,
  reloadSources,
}: {
  source: NativeSource
  reloadSources: () => Promise<unknown>
}) {
  const [pages, setPages] = useState<string[]>([""])
  const [failure, setFailure] = useState(false)
  const [busy, setBusy] = useState(false)
  const inventory = useQuery(useNativeRecoverySourcesInventory, {
    path: { id: source.id },
    query: { before: pages.at(-1) || undefined, limit: 50 },
  })
  const refresh = useMutation(useNativeRecoverySourcesRefresh)
  const observed = inventory.data?.source ?? source
  async function refreshCloud() {
    if (busy) return
    setBusy(true)
    setFailure(false)
    try {
      await refresh.mutateAsync({ path: { id: source.id } })
      setPages([""])
    } catch {
      setFailure(true)
    } finally {
      await Promise.allSettled([inventory.refetch(), reloadSources()])
      setBusy(false)
    }
  }
  return (
    <div className="grid min-w-0 gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="min-w-0 text-xs break-all text-muted-foreground">
          {source.provider} · {source.region} · {source.physicalId}
        </p>
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => void refreshCloud()}
        >
          {busy ? "Reading cloud snapshots…" : "Refresh cloud snapshots"}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {observed.observedAt
          ? `Saved ${formatDate(observed.observedAt)}`
          : "No successful observation yet"}{" "}
        · {observed.complete ? "complete scan" : "partial scan or not checked"}
        {observed.state === "failed" ? " · last refresh failed" : ""}
      </p>
      {failure && (
        <Alert variant="destructive">
          <AlertDescription>
            Could not refresh cloud snapshots. Saved observations remain below.
            Check the original account and snapshot permissions, then retry.
          </AlertDescription>
        </Alert>
      )}
      {inventory.status === "error" && (
        <QueryError
          error={inventory.error}
          fallback="Could not read saved snapshot observations"
          retry={inventory.refetch}
        />
      )}
      {inventory.status === "pending" && !inventory.data ? (
        <p role="status">Loading saved snapshots…</p>
      ) : inventory.data?.points.length ? (
        <ul aria-label="Native snapshot observations" className="divide-y">
          {inventory.data.points.map((point) => (
            <li key={point.id} className="grid min-w-0 gap-1 py-2 text-sm">
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                <span className="min-w-0 font-medium break-all">
                  {point.name}
                </span>
                <span className="text-xs text-muted-foreground">
                  {point.present ? point.status : "No longer observed"} ·{" "}
                  {point.kind} · {point.encrypted ? "encrypted" : "unencrypted"}
                </span>
              </div>
              <p className="text-xs text-muted-foreground">
                Created {formatDate(point.createdAt)} · {point.engine}{" "}
                {point.version}
              </p>
              <details className="text-xs">
                <summary className="cursor-pointer text-muted-foreground">
                  Snapshot identity
                </summary>
                <p className="mt-1 break-all">{point.providerId}</p>
                <p className="mt-1 text-muted-foreground">
                  Last observed {formatDate(point.lastSeenAt)}
                </p>
              </details>
            </li>
          ))}
        </ul>
      ) : inventory.status !== "error" ? (
        <NativeEmpty title="No saved native snapshots">
          Refresh the original cloud account to inspect its manual and automated
          snapshots. An empty or partial inventory does not prove
          recoverability.
        </NativeEmpty>
      ) : null}
      {(pages.length > 1 || inventory.data?.nextBefore) && (
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={busy || pages.length === 1}
            onClick={() => setPages((current) => current.slice(0, -1))}
          >
            Previous snapshots
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={
              busy ||
              !inventory.data?.nextBefore ||
              inventory.status !== "success"
            }
            onClick={() =>
              setPages((current) => [...current, inventory.data!.nextBefore!])
            }
          >
            More snapshots
          </Button>
        </div>
      )}
    </div>
  )
}
