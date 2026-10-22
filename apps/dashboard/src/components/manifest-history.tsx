// The manifest's revisions, each with what it changed from the one before.

import { HistoryIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { useManifestsHistory } from "@/client/forge/loomplane/hooks"
import type { Revision } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { changeCount, diffLines, hunks, type DiffLine } from "@/lib/diff"
import { formatDate } from "@/lib/format"
import { cn } from "@/lib/utils"

/** ManifestHistory opens the project's revisions, newest first. */
export function ManifestHistory({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        variant="ghost"

        onClick={() => setOpen(true)}
      >
        <HistoryIcon />
        History
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>Manifest history</DialogTitle>
            <DialogDescription>
              Every save is a revision, and each plan names the one it was made
              from. Pick one to see what it changed.
            </DialogDescription>
          </DialogHeader>
          {open && <Revisions projectId={projectId} />}
        </DialogContent>
      </Dialog>
    </>
  )
}

function Revisions({ projectId }: { projectId: string }) {
  const history = useQuery(useManifestsHistory, { path: { id: projectId } })
  const revisions = history.data?.revisions ?? []
  const [picked, setPicked] = useState<number | undefined>()
  if (history.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(history.error, "The history couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!history.data) return <Skeleton className="h-64 w-full rounded-lg" />

  const index = Math.max(
    0,
    revisions.findIndex((r) => r.revision === picked)
  )
  const current = revisions[index]
  const previous = revisions[index + 1]
  return (
    <div className="grid gap-4 md:grid-cols-[12rem_1fr]">
      <ol className="grid max-h-[28rem] content-start gap-1 overflow-y-auto">
        {revisions.map((r, i) => (
          <li key={r.id}>
            <button
              type="button"
              onClick={() => setPicked(r.revision)}
              className={cn(
                "grid w-full gap-0.5 rounded-md px-2.5 py-2 text-left text-sm hover:bg-muted",
                i === index && "bg-muted"
              )}
            >
              <span className="font-medium tabular-nums">
                Revision {r.revision}
              </span>
              <span className="text-xs text-muted-foreground">
                {formatDate(r.createdAt)}
              </span>
            </button>
          </li>
        ))}
      </ol>
      {current && <Changes current={current} previous={previous} />}
    </div>
  )
}

function Changes({
  current,
  previous,
}: {
  current: Revision
  previous?: Revision
}) {
  const lines = diffLines(previous?.yaml ?? "", current.yaml)
  const { added, removed } = changeCount(lines)
  return (
    <div className="grid min-w-0 content-start gap-2">
      <p className="text-sm text-muted-foreground">
        {previous
          ? `Revision ${current.revision} against ${previous.revision}: ${added} lines added, ${removed} removed.`
          : `Revision ${current.revision}, the first.`}
      </p>
      <pre className="max-h-[26rem] overflow-auto rounded-lg border bg-muted/30 py-2 font-mono text-xs leading-relaxed">
        {hunks(lines).map((h, i) =>
          h.kind === "folded" ? (
            <div
              // Hunks are fixed for a revision, so their place is their identity.
              key={i}
              className="my-1 border-y border-dashed px-3 py-0.5 text-muted-foreground select-none"
            >
              {h.count} unchanged {h.count === 1 ? "line" : "lines"}
            </div>
          ) : (
            h.lines.map((l, j) => <Line key={`${i}.${j}`} line={l} />)
          )
        )}
      </pre>
    </div>
  )
}

function Line({ line }: { line: DiffLine }) {
  return (
    <div
      className={cn(
        "px-3 whitespace-pre",
        line.kind === "added" &&
          "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
        line.kind === "removed" &&
          "bg-red-500/10 text-red-700 line-through decoration-red-500/40 dark:text-red-300"
      )}
    >
      <span aria-hidden className="mr-3 opacity-50 select-none">
        {line.kind === "added" ? "+" : line.kind === "removed" ? "-" : " "}
      </span>
      {line.text}
    </div>
  )
}
