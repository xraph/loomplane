import { useState } from "react"

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
import { NativeEmpty } from "@/components/native-recovery-empty"
import { NativeCopyButton } from "@/components/native-copy-review"
import { NativeCopyHistory } from "@/components/native-copies"
import { useOrganization } from "@/lib/organization"
import { formatDate } from "@/lib/format"

export function NativeRecovery(props: {
  project: string
  environment?: string
}) {
  const { session } = useOrganization()
  return (
    <NativeRecoveryContent
      key={`${session?.orgId ?? ""}:${props.project}:${props.environment ?? ""}`}
      {...props}
    />
  )
}

function NativeRecoveryContent({
  project,
  environment,
}: {
  project: string
  environment?: string
}) {
  const [pages, setPages] = useState<string[]>([""])
  const [chosen, setChosen] = useState("")
  const [copyVersion, setCopyVersion] = useState(0)
  const sources = useQuery(useNativeRecoverySourcesList, {
    path: { id: project },
    query: {
      before: pages.at(-1) || undefined,
      limit: 50,
      environment_id: environment,
    },
  })
  const visible = (sources.data?.sources ?? []).filter(
    (source) => !environment || source.environmentId === environment
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
        listed here has not been restore-tested. You can review temporary Cloud
        SQL PostgreSQL 17 copies below. PITR and RDS restore controls remain in
        your cloud account.
      </p>
      <NativeCopyHistory
        key={copyVersion}
        project={project}
        environment={environment}
      />
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
            onCopyAccepted={() => setCopyVersion((version) => version + 1)}
          />
        </>
      ) : sources.status !== "error" ? (
        <NativeEmpty title="No captured cloud database history">
          Pinned RDS and Cloud SQL histories appear after deployment. Older
          unpinned databases need an explicit identity check first. You can
          deploy a managed database in Services.
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
  onCopyAccepted,
}: {
  source: NativeSource
  reloadSources: () => Promise<unknown>
  onCopyAccepted: () => void
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
      {source.provider === "cloud-sql" && !observed.complete && (
        <p className="text-xs text-muted-foreground">
          Cloud SQL points need matching source creation metadata. Missing
          proof, provider warnings or a scan limit leave this scan partial.
          Earlier observations stay visible.
        </p>
      )}
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
                {source.provider === "cloud-sql" ? "Point time" : "Created"}{" "}
                {formatDate(point.createdAt)} · {point.engine} {point.version}
                {" · "}
                <span>
                  {point.expiresAt
                    ? `Provider expiry ${formatDate(point.expiresAt)}`
                    : "Expiry not reported"}
                </span>
              </p>
              <NativeCopyButton
                source={source}
                point={point}
                onAccepted={onCopyAccepted}
                disabled={busy || inventory.status !== "success"}
              />
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
