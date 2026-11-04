import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
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
import { hostEvidenceIssues } from "@/lib/platform-cloud"

type Dialogue =
  { kind: "add" } | { kind: "allotment" | "retire" | "move"; host: HostView }

const workloads = (n: number) => `${n} ${n === 1 ? "workload" : "workloads"}`

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

  const regionName = (id: string) =>
    regions.data?.regions.find((r) => r.id === id)?.displayName ?? id

  const run = async (
    work: () => Promise<unknown>,
    done: string,
    fail: string
  ) => {
    setError("")
    try {
      await work()
      invalidate(usePlatformCloudHostsList)
      toast.success(done)
    } catch (err) {
      setError(messageOf(err, fail))
    }
  }

  const doMove = async (h: HostView) => {
    setError("")
    try {
      // The operation has no typed response codec, so the result is untyped.
      const r = (await moveAll.mutateAsync({
        path: { host: h.id },
      })) as MoveAll
      invalidate(usePlatformCloudHostsList)
      if (r.failed.length > 0)
        toast.error(
          `${r.moved} moved. ${r.failed.length} could not be moved: ${r.failed.join(", ")}`
        )
      else toast.success(`${r.moved} moved`)
    } catch (err) {
      setError(messageOf(err, "The workloads weren't moved."))
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
            {error || messageOf(hosts.error, "Hosts couldn't be loaded.")}
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
              const issues = hostEvidenceIssues(
                h.evidence,
                new Date(),
                evidenceMaxAgeMs
              )
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
                    <Badge variant="outline">{h.status}</Badge>
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
                    <div className="flex flex-wrap gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Check ${h.hostId}`}
                        onClick={() =>
                          run(
                            () => check.mutateAsync({ path: { host: h.id } }),
                            "Check finished",
                            "The check failed."
                          )
                        }
                      >
                        Check
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Drain ${h.hostId}`}
                        onClick={() =>
                          run(
                            () => drain.mutateAsync({ path: { host: h.id } }),
                            "Host is draining",
                            "The host wasn't drained."
                          )
                        }
                      >
                        Drain
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Move all from ${h.hostId}`}
                        onClick={() => setOpen({ kind: "move", host: h })}
                      >
                        Move all
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Edit allotment for ${h.hostId}`}
                        onClick={() => setOpen({ kind: "allotment", host: h })}
                      >
                        Edit allotment
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Retire ${h.hostId}`}
                        disabled={h.placements > 0}
                        onClick={() => setOpen({ kind: "retire", host: h })}
                      >
                        Retire
                      </Button>
                    </div>
                    {h.placements > 0 && (
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
      {open?.kind === "move" && (
        <Confirm
          title={`Move all workloads off ${open.host.hostId}?`}
          description="Each workload is moved to another host in the region. Some may be skipped."
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
