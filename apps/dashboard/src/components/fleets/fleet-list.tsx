import { useId, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { InstanceTable, type InstanceRow } from "./instances"

export type FleetQuery = {
  after: string
  status: string
  external_ref: string
}

export function FleetList({
  projectID,
  rows,
  nextCursor,
  query,
  onQuery,
  busy = false,
  content,
}: {
  projectID: string
  rows: InstanceRow[]
  nextCursor?: string
  query: FleetQuery
  onQuery: (query: FleetQuery) => void
  busy?: boolean
  content?: ReactNode
}) {
  const errorID = useId()
  const [referenceError, setReferenceError] = useState(false)
  const [reference, setReference] = useState(query.external_ref)
  const [status, setStatus] = useState(query.status)
  const clear = () => {
    setReferenceError(false)
    setReference("")
    setStatus("")
    onQuery({ after: "", status: "", external_ref: "" })
  }
  return (
    <section
      className="grid min-w-0 gap-3"
      aria-label="Fleet browser"
      aria-busy={busy}
    >
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          const tooLong =
            new TextEncoder().encode(reference.trim()).length > 128
          setReferenceError(tooLong)
          if (!tooLong)
            onQuery({ after: "", status, external_ref: reference.trim() })
        }}
      >
        <label className="grid min-w-0 flex-[1_1_12rem] gap-1 text-xs sm:max-w-72">
          Instance reference
          <Input
            value={reference}
            maxLength={128}
            aria-invalid={referenceError || undefined}
            aria-describedby={referenceError ? errorID : undefined}
            onChange={(event) => setReference(event.target.value)}
            placeholder="Exact reference"
          />
        </label>
        <label className="grid gap-1 text-xs">
          Instance state
          <select
            value={status}
            onChange={(event) => setStatus(event.target.value)}
            className="h-[34px] rounded-lg border border-input bg-background px-2 text-[13px] focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">All states</option>
            {[
              "waiting_approval",
              "queued",
              "provisioning",
              "deploying",
              "ready",
              "degraded",
              "failed",
              "suspended",
              "retired",
            ].map((value) => (
              <option key={value} value={value}>
                {value.replaceAll("_", " ")}
              </option>
            ))}
          </select>
        </label>
        <Button type="submit" variant="outline" disabled={busy}>
          Apply filters
        </Button>
        {(query.status || query.external_ref) && (
          <Button variant="ghost" onClick={clear} disabled={busy}>
            Clear filters
          </Button>
        )}
      </form>
      {referenceError && (
        <p id={errorID} role="alert" className="text-sm text-destructive">
          This reference is too long. Shorten it and try again.
        </p>
      )}
      {!content && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <span>{rows.length} on this page</span>
          <span>Observations may be unavailable</span>
        </div>
      )}
      {content ?? (
        <InstanceTable
          projectID={projectID}
          rows={rows}
          filtered={!!(query.status || query.external_ref || query.after)}
          onClear={clear}
        />
      )}
      <nav
        aria-label="Instance pages"
        className="flex flex-wrap justify-end gap-2"
      >
        <Button
          size="sm"
          variant="outline"
          disabled={!query.after || busy}
          onClick={() => onQuery({ ...query, after: "" })}
        >
          First page
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={!nextCursor || busy}
          onClick={() => onQuery({ ...query, after: nextCursor ?? "" })}
        >
          Next page
        </Button>
      </nav>
    </section>
  )
}
