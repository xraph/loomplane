import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudHostsCreate,
  usePlatformCloudHostsList,
  usePlatformCloudHostsSetAllotment,
  usePlatformCloudRegionsList,
} from "@/client/forge/loomplane/hooks"
import type { HostView, NewHost } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CopyButton } from "@/components/keyless-setup"
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
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"

type Sizes = { vcpus: string; mem: string; disk: string; vms: string }

function SizeFields({
  sizes,
  onChange,
}: {
  sizes: Sizes
  onChange: (s: Sizes) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-3">
      <NumberField
        label="vCPUs"
        value={sizes.vcpus}
        onChange={(vcpus) => onChange({ ...sizes, vcpus })}
      />
      <NumberField
        label="Memory (GiB)"
        value={sizes.mem}
        onChange={(mem) => onChange({ ...sizes, mem })}
      />
      <NumberField
        label="Disk (GiB)"
        value={sizes.disk}
        onChange={(disk) => onChange({ ...sizes, disk })}
      />
      <NumberField
        label="VMs"
        value={sizes.vms}
        onChange={(vms) => onChange({ ...sizes, vms })}
      />
    </div>
  )
}

const bodyOf = (s: Sizes) => ({
  vcpus: toCount(s.vcpus),
  memoryMib: toCount(s.mem) * 1024,
  diskMib: toCount(s.disk) * 1024,
  vms: toCount(s.vms),
})

/**
 * AddHostDialog registers a host. The create response carries the client
 * certificate once, so it lives only in this dialog's state. The caller
 * mounts the dialog only while it is open, so closing drops the secret.
 */
export function AddHostDialog({ onClose }: { onClose: () => void }) {
  const regions = useQuery(usePlatformCloudRegionsList)
  const create = useMutation(usePlatformCloudHostsCreate)
  const invalidate = useInvalidate()
  const regionList = regions.data?.regions ?? []
  const [regionId, setRegionId] = useState("")
  const [endpoint, setEndpoint] = useState("")
  const [hostId, setHostId] = useState("")
  const [serverCa, setServerCa] = useState("")
  const [sizes, setSizes] = useState<Sizes>({
    vcpus: "8",
    mem: "32",
    disk: "200",
    vms: "20",
  })
  const [error, setError] = useState("")
  const [made, setMade] = useState<NewHost | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      const res = await create.mutateAsync({
        body: {
          regionId,
          endpoint: endpoint.trim(),
          hostId: hostId.trim(),
          serverCa: serverCa.trim(),
          allotment: bodyOf(sizes),
        },
      })
      setMade(res as NewHost)
      invalidate(usePlatformCloudHostsList)
      toast.success("Host added")
    } catch (err) {
      setError(messageOf(err, "The host wasn't added."))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add host</DialogTitle>
          <DialogDescription>
            {made
              ? "Install this client certificate on the host now. It is shown once and can't be shown again."
              : "Register a host in a region. Its client certificate is shown once after you add it."}
          </DialogDescription>
        </DialogHeader>
        {made ? (
          <div className="flex flex-col gap-3">
            <Field>
              <FieldLabel>Client fingerprint</FieldLabel>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 text-sm break-all">
                  {made.clientFingerprint}
                </code>
                <CopyButton
                  value={made.clientFingerprint}
                  label="Copy fingerprint"
                />
              </div>
            </Field>
            <Field>
              <FieldLabel>Client certificate</FieldLabel>
              <Textarea
                readOnly
                aria-label="Client certificate"
                value={made.clientCertificate}
                className="font-mono text-xs"
                rows={8}
              />
              <div>
                <CopyButton
                  value={made.clientCertificate}
                  label="Copy certificate"
                />
              </div>
            </Field>
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-3">
            <FieldGroup>
              <Field>
                <FieldLabel>Region</FieldLabel>
                <Select
                  items={regionList.map((r) => ({
                    label: r.displayName,
                    value: r.id,
                  }))}
                  value={regionId || null}
                  onValueChange={(v) => setRegionId(String(v))}
                >
                  <SelectTrigger className="w-full" aria-label="Region">
                    <SelectValue placeholder="Choose a region" />
                  </SelectTrigger>
                  <SelectContent>
                    {regionList.map((r) => (
                      <SelectItem key={r.id} value={r.id}>
                        {r.displayName}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {regions.data && regionList.length === 0 && (
                  <FieldDescription>Add a region first</FieldDescription>
                )}
              </Field>
              <Field>
                <FieldLabel>Endpoint</FieldLabel>
                <Input
                  aria-label="Endpoint"
                  value={endpoint}
                  onChange={(e) => setEndpoint(e.target.value)}
                  placeholder="host.example.com:8443"
                />
              </Field>
              <Field>
                <FieldLabel>Host ID</FieldLabel>
                <Input
                  aria-label="Host ID"
                  value={hostId}
                  onChange={(e) => setHostId(e.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel>Server CA</FieldLabel>
                <Textarea
                  aria-label="Server CA"
                  value={serverCa}
                  onChange={(e) => setServerCa(e.target.value)}
                  className="font-mono text-xs"
                  rows={4}
                />
              </Field>
              <SizeFields sizes={sizes} onChange={setSizes} />
            </FieldGroup>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={
                  create.isPending ||
                  !regionId ||
                  !endpoint.trim() ||
                  !hostId.trim()
                }
              >
                Add host
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** AllotmentDialog edits how much of a host workspaces may use. */
export function AllotmentDialog({
  host,
  onClose,
}: {
  host: HostView
  onClose: () => void
}) {
  const save = useMutation(usePlatformCloudHostsSetAllotment)
  const invalidate = useInvalidate()
  const [sizes, setSizes] = useState<Sizes>({
    vcpus: String(host.allotment.vcpus),
    mem: String(gibOf(host.allotment.memoryMib)),
    disk: String(gibOf(host.allotment.diskMib)),
    vms: String(host.allotment.vms),
  })
  const [error, setError] = useState("")
  const next = bodyOf(sizes)
  const below =
    next.vcpus < host.use.vcpus ||
    next.memoryMib < host.use.memoryMib ||
    next.vms < host.use.vms ||
    next.diskMib < host.use.diskMib

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await save.mutateAsync({ path: { host: host.id }, body: next })
      invalidate(usePlatformCloudHostsList)
      toast.success("Allotment saved")
      onClose()
    } catch (err) {
      setError(messageOf(err, "The allotment wasn't saved."))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit allotment</DialogTitle>
          <DialogDescription>{host.hostId}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <SizeFields sizes={sizes} onChange={setSizes} />
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
