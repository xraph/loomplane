import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudTenantsList,
  usePlatformCloudTenantsSet,
} from "@/client/forge/loomplane/hooks"
import type { TenantRow } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { NumberField, gibOf, toCount } from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"

/** LimitsDialog edits one workspace's limits. Lowering below use is allowed, with a note. */
export function LimitsDialog({
  row,
  onClose,
}: {
  row: TenantRow
  onClose: () => void
}) {
  const save = useMutation(usePlatformCloudTenantsSet)
  const invalidate = useInvalidate()
  const [v, setV] = useState({
    vcpus: String(row.limits.vcpus),
    mem: String(gibOf(row.limits.memoryMib)),
    vms: String(row.limits.vms),
    builds: String(row.limits.concurrentBuilds),
  })
  const [error, setError] = useState("")
  const limits = {
    vcpus: toCount(v.vcpus),
    memoryMib: toCount(v.mem) * 1024,
    vms: toCount(v.vms),
    concurrentBuilds: toCount(v.builds),
  }
  const below =
    limits.vcpus < row.use.vcpus ||
    limits.memoryMib < row.use.memoryMib ||
    limits.vms < row.use.vms ||
    limits.concurrentBuilds < row.use.concurrentBuilds

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await save.mutateAsync({
        path: { org: row.orgId },
        body: { status: row.tenant?.status ?? "disabled", limits },
      })
      invalidate(usePlatformCloudTenantsList)
      toast.success("Limits saved")
      onClose()
    } catch (err) {
      setError(messageOf(err, "The limits weren't saved."))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit limits</DialogTitle>
          <DialogDescription>{row.orgName}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <div className="grid grid-cols-2 gap-3">
            <NumberField
              label="vCPUs"
              value={v.vcpus}
              onChange={(vcpus) => setV({ ...v, vcpus })}
            />
            <NumberField
              label="Memory (GiB)"
              value={v.mem}
              onChange={(mem) => setV({ ...v, mem })}
            />
            <NumberField
              label="VMs"
              value={v.vms}
              onChange={(vms) => setV({ ...v, vms })}
            />
            <NumberField
              label="Builds at once"
              value={v.builds}
              onChange={(builds) => setV({ ...v, builds })}
            />
          </div>
          {below && (
            <p className="text-sm text-muted-foreground">
              Running work keeps running. New work is refused until use is under
              the limit.
            </p>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
