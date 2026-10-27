import { useState } from "react"

import { useQuery } from "@/client/hooks"
import { useNativeRecoverySourcesPitrWindow } from "@/client/forge/loomplane/hooks"
import type { NativeSource } from "@/client/forge/loomplane/types"
import { NativeCopyReview } from "@/components/native-copy-review"
import { NativeEmpty } from "@/components/native-recovery-empty"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useOrganization } from "@/lib/organization"
import { coveredPITRTime } from "@/lib/native-pitr-time"

export function NativePITR({
  source,
  onAccepted,
}: {
  source: NativeSource
  onAccepted: () => void
}) {
  const [opened, setOpened] = useState(false)
  return (
    <div className="grid min-w-0 gap-2">
      {!opened ? (
        <Button size="sm" variant="outline" onClick={() => setOpened(true)}>
          Read PITR window
        </Button>
      ) : (
        <PITRWindow key={source.id} source={source} onAccepted={onAccepted} />
      )}
    </div>
  )
}

function PITRWindow({
  source,
  onAccepted,
}: {
  source: NativeSource
  onAccepted: () => void
}) {
  const { can } = useOrganization()
  const query = useQuery(useNativeRecoverySourcesPitrWindow, {
    path: { id: source.id },
  })
  const [at, setAt] = useState("")
  const [reviewing, setReviewing] = useState(false)
  const window = query.data
  const valid =
    query.status === "success" &&
    !!window?.available &&
    !!window.pointId &&
    coveredPITRTime(at, window.earliest, window.latest, source.provider)
  return (
    <div className="grid min-w-0 gap-2 rounded-xl bg-card p-3 ring-1 ring-border">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-[13.5px] font-semibold">
          Point-in-time recovery
        </span>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => void query.refetch().catch(() => undefined)}
        >
          Refresh PITR window
        </Button>
      </div>
      {query.status === "error" ? (
        <QueryError
          error={query.error}
          fallback="Could not read the current PITR window"
          retry={query.refetch}
        />
      ) : !window ? (
        <p role="status" className="text-xs">
          Reading provider recovery window…
        </p>
      ) : !window.available ? (
        <NativeEmpty title="PITR unavailable">
          {window.unavailableReason ||
            "This source has no supported recovery window."}
        </NativeEmpty>
      ) : (
        <>
          <p className="text-xs break-all text-muted-foreground">
            From {window.earliest} to before {window.latest}. Observed{" "}
            {window.observedAt}.
          </p>
          {can("service_restore:manage") && (
            <>
              <div className="flex flex-wrap items-end gap-2">
                <div className="grid min-w-0 flex-1 gap-1">
                  <Label htmlFor="native-pitr-time">Restore time in UTC</Label>
                  <Input
                    id="native-pitr-time"
                    value={at}
                    placeholder="2026-10-22T12:00:03Z"
                    autoComplete="off"
                    onChange={(event) => setAt(event.target.value)}
                  />
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!valid}
                  onClick={() => setReviewing(true)}
                >
                  Review PITR copy
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                {source.provider === "cloud-sql"
                  ? "Choose a UTC time within this window, with up to nine fractional digits."
                  : "Choose a whole UTC second within this window."}{" "}
                The provider checks the window again before creation. No time is
                selected automatically.
              </p>
            </>
          )}
        </>
      )}
      {reviewing && valid && can("service_restore:manage") && (
        <NativeCopyReview
          source={source}
          point={{ id: window!.pointId!, name: source.name }}
          restoreTime={at}
          onAccepted={onAccepted}
          onClose={() => setReviewing(false)}
        />
      )}
    </div>
  )
}
