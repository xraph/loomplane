import { useState } from "react"
import { toast } from "sonner"

import {
  usePlatformCloudHostsCheck,
  usePlatformCloudHostsDrain,
  usePlatformCloudHostsList,
  usePlatformCloudHostsMoveAll,
  usePlatformCloudHostsRetire,
  usePlatformCloudRegionsList,
} from "@/client/forge/loomplane/hooks"
import type { HostView, MoveAll } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  AllotmentDialog,
  AddHostDialog,
} from "@/components/platform-cloud-host-dialogs"
import {
  Confirm,
  errorOf,
  evidenceMaxAgeMs,
  gibOf,
} from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { hostEvidenceIssues, statusLabel } from "@/lib/platform-cloud"

type Dialogue =
  | { kind: "add" }
  | { kind: "allotment" | "retire" | "move" | "drain"; host: HostView }

const workloads = (n: number) => `${n} ${n === 1 ? "workload" : "workloads"}`

const oneWay = "The host stops taking new work. This can't be undone here."

/** moveSummary puts what Move all did into one sentence per outcome. */
function moveSummary(r: MoveAll): string {
  const parts = [`${r.moved} moved`]
  const skipped = r.skipped ?? []
  const failed = r.failed ?? []
  if (skipped.length > 0)
    parts.push(
      `${skipped.length} stayed for lack of room: ${skipped.join(", ")}`
    )
  if (failed.length > 0)
    parts.push(`${failed.length} couldn't be moved: ${failed.join(", ")}`)
  return parts.join(". ")
}

/** PlatformCloudHosts lists the shared hosts and their row actions. */
export function PlatformCloudHosts() {
  const hosts = useQuery(usePlatformCloudHostsList)
  const regions = useQuery(usePlatformCloudRegionsList)
  const check = useMutation(usePlatformCloudHostsCheck)
  const drain = useMutation(usePlatformCloudHostsDrain)
  const moveAll = useMutation(usePlatformCloudHostsMoveAll)
  const retire = useMutation(usePlatformCloudHostsRetire)
  const invalidate = useInvalidate()
  const [open, setOpen] = useState<Dialogue | null>(null)
  const [error, setError] = useState("")
  // busy holds an "action:host" key for each row action still running.
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const mark = (key: string, on: boolean) =>
    setBusy((b) => {
      const next = new Set(b)
      if (on) next.add(key)
      else next.delete(key)
      return next
    })

  const regionName = (id: string) =>
    regions.data?.regions.find((r) => r.id === id)?.displayName ?? id

  const run = async (
    key: string,
    work: () => Promise<unknown>,
    done: string,
    fail: string
  ) => {
    setError("")
    mark(key, true)
    try {
      await work()
      invalidate(usePlatformCloudHostsList)
      toast.success(done)
    } catch (err) {
      setError(errorOf(err, fail))
    } finally {
      mark(key, false)
    }
  }

  // Move all can take minutes: each service gets up to half an hour.
  const doMove = async (h: HostView) => {
    setError("")
    mark(`move:${h.id}`, true)
    try {
      // The operation has no typed response codec, so the result is untyped.
      const r = (await moveAll.mutateAsync({
        path: { host: h.id },
      })) as MoveAll
      const text = moveSummary(r)
      if ((r.failed ?? []).length > 0) toast.error(text)
      else if ((r.skipped ?? []).length > 0) toast.warning(text)
      else toast.success(text)
    } catch (err) {
      setError(errorOf(err, "The workloads weren't moved."))
    } finally {
      invalidate(usePlatformCloudHostsList)
      mark(`move:${h.id}`, false)
    }
  }

  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-medium">Hosts</h2>
        <Button size="sm" onClick={() => setOpen({ kind: "add" })}>
          Add host
        </Button>
      </div>
      {(error || hosts.status === "error") && (
        <Alert variant="destructive">
          <AlertDescription>
            {error || errorOf(hosts.error, "Hosts couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!hosts.data && hosts.status !== "error" && (
        <Skeleton className="h-24 w-full rounded-xl" />
      )}
      {hosts.data && hosts.data.hosts.length === 0 && (
        <p className="text-sm text-muted-foreground">No hosts yet.</p>
      )}
      {hosts.data && hosts.data.hosts.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Host</TableHead>
              <TableHead>Region</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Allotment and use</TableHead>
              <TableHead>Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {hosts.data.hosts.map((h) => {
              const retired = h.status === "retired"
              const draining = h.status === "draining"
              const moving = busy.has(`move:${h.id}`)
              const issues = retired
                ? []
                : hostEvidenceIssues(h.evidence, new Date(), evidenceMaxAgeMs)
              return (
                <TableRow key={h.id}>
                  <TableCell>
                    <div className="font-medium">{h.hostId}</div>
                    <div className="text-xs text-muted-foreground">
                      {h.endpoint}
                    </div>
                  </TableCell>
                  <TableCell>{regionName(h.regionId)}</TableCell>
                  <TableCell>
                    <Badge variant="outline">
                      {statusLabel("host", h.status)}
                    </Badge>
                    {issues.length > 0 && (
                      <ul className="mt-1 text-xs text-destructive">
                        {issues.map((i) => (
                          <li key={i}>{i}</li>
                        ))}
                      </ul>
                    )}
                  </TableCell>
                  <TableCell className="text-sm">
                    <div>
                      {h.use.vcpus} of {h.allotment.vcpus} vCPUs
                    </div>
                    <div>
                      {gibOf(h.use.memoryMib)} of {gibOf(h.allotment.memoryMib)}{" "}
                      GiB memory
                    </div>
                    <div>
                      {h.use.vms} of {h.allotment.vms} VMs
                    </div>
                    <div className="text-muted-foreground">
                      {workloads(h.placements)}
                    </div>
                  </TableCell>
                  <TableCell>
                    {!retired && (
                      <div className="flex flex-wrap gap-1">
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Check ${h.hostId}`}
                          disabled={busy.has(`check:${h.id}`)}
                          onClick={() =>
                            run(
                              `check:${h.id}`,
                              () => check.mutateAsync({ path: { host: h.id } }),
                              "Check finished",
                              "The check failed."
                            )
                          }
                        >
                          Check
                        </Button>
                        {!draining && (
                          <Button
                            size="sm"
                            variant="outline"
                            aria-label={`Drain ${h.hostId}`}
                            disabled={busy.has(`drain:${h.id}`)}
                            onClick={() => setOpen({ kind: "drain", host: h })}
                          >
                            Drain
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Move all from ${h.hostId}`}
                          disabled={moving}
                          onClick={() => setOpen({ kind: "move", host: h })}
                        >
                          {moving ? "Moving" : "Move all"}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Edit allotment for ${h.hostId}`}
                          onClick={() =>
                            setOpen({ kind: "allotment", host: h })
                          }
                        >
                          Edit allotment
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Retire ${h.hostId}`}
                          disabled={
                            !draining ||
                            h.placements > 0 ||
                            busy.has(`retire:${h.id}`)
                          }
                          onClick={() => setOpen({ kind: "retire", host: h })}
                        >
                          Retire
                        </Button>
                      </div>
                    )}
                    {moving && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        Moving workloads. This can take several minutes.
                      </div>
                    )}
                    {!retired && !draining && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        Drain it first
                      </div>
                    )}
                    {draining && h.placements > 0 && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        Move or remove its {workloads(h.placements)} first
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
      {open?.kind === "add" && <AddHostDialog onClose={() => setOpen(null)} />}
      {open?.kind === "allotment" && (
        <AllotmentDialog host={open.host} onClose={() => setOpen(null)} />
      )}
      {open?.kind === "drain" && (
        <Confirm
          title={`Drain ${open.host.hostId}?`}
          description={oneWay}
          action="Drain"
          onClose={() => setOpen(null)}
          onConfirm={() => {
            const h = open.host
            setOpen(null)
            void run(
              `drain:${h.id}`,
              () => drain.mutateAsync({ path: { host: h.id } }),
              "Host is draining",
              "The host wasn't drained."
            )
          }}
        />
      )}
      {open?.kind === "move" && (
        <Confirm
          title={`Move all workloads off ${open.host.hostId}?`}
          description={`Each workload is moved to another host in the region. One stays where it is when no other host has room. ${oneWay}`}
          action="Move all"
          onClose={() => setOpen(null)}
          onConfirm={() => {
            const h = open.host
            setOpen(null)
            void doMove(h)
          }}
        />
      )}
      {open?.kind === "retire" && (
        <Confirm
          title={`Retire ${open.host.hostId}?`}
          description="The host stops taking work and leaves the list. This can't be undone."
          action="Retire"
          onClose={() => setOpen(null)}
          onConfirm={() => {
            const h = open.host
            setOpen(null)
            void run(
              `retire:${h.id}`,
              () => retire.mutateAsync({ path: { host: h.id } }),
              "Host retired",
              "The host wasn't retired."
            )
          }}
        />
      )}
    </section>
  )
}
