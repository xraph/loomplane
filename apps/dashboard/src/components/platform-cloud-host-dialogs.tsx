import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import {
  usePlatformCloudHostsCreate,
  usePlatformCloudHostsList,
  usePlatformCloudHostsSetAllotment,
  usePlatformCloudRegionsList,
} from "@/client/forge/loomplane/hooks"
import type { HostView, NewHost } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CopyButton } from "@/components/keyless-setup"
import {
  NumberField,
  countOf,
  errorOf,
  gibOf,
  mibOf,
} from "@/components/platform-cloud-parts"
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

type Allotment = HostView["allotment"]

/**
 * bodyOf reads the size fields, or null while one isn't a whole number.
 * Given what was shown and saved, memory and disk the owner didn't touch
 * keep their stored MiB instead of the rounded GiB.
 */
function bodyOf(s: Sizes, shown?: Sizes, was?: Allotment): Allotment | null {
  const vcpus = countOf(s.vcpus)
  const vms = countOf(s.vms)
  const memoryMib = mibOf(
    s.mem,
    shown && was && { shown: shown.mem, mib: was.memoryMib }
  )
  const diskMib = mibOf(
    s.disk,
    shown && was && { shown: shown.disk, mib: was.diskMib }
  )
  if (vcpus === null || vms === null || memoryMib === null || diskMib === null)
    return null
  return { vcpus, memoryMib, diskMib, vms }
}

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

  const allotment = bodyOf(sizes)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!allotment) return
    setError("")
    try {
      const res = await create.mutateAsync({
        body: {
          regionId,
          endpoint: endpoint.trim(),
          hostId: hostId.trim(),
          serverCa: serverCa.trim(),
          allotment,
        },
      })
      setMade(res as NewHost)
      invalidate(usePlatformCloudHostsList)
      toast.success("Host added")
    } catch (err) {
      setError(errorOf(err, "The host wasn't added."))
    }
  }

  // Once the host is made, the certificate is on screen for the only time,
  // so Escape and outside clicks don't close it: only Done does.
  return (
    <Dialog
      open
      disablePointerDismissal={!!made}
      onOpenChange={(o) => {
        if (!o && !made) onClose()
      }}
    >
      <DialogContent showCloseButton={!made}>
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
            <p className="text-sm text-muted-foreground">
              Once the host agent is up with this certificate, run Check on the
              host.
            </p>
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
                  !hostId.trim() ||
                  !serverCa.trim() ||
                  !allotment
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
  const [shown] = useState<Sizes>(() => ({
    vcpus: String(host.allotment.vcpus),
    mem: String(gibOf(host.allotment.memoryMib)),
    disk: String(gibOf(host.allotment.diskMib)),
    vms: String(host.allotment.vms),
  }))
  const [sizes, setSizes] = useState<Sizes>(shown)
  const [error, setError] = useState("")
  const next = bodyOf(sizes, shown, host.allotment)
  const below =
    !!next &&
    (next.vcpus < host.use.vcpus ||
      next.memoryMib < host.use.memoryMib ||
      next.vms < host.use.vms ||
      next.diskMib < host.use.diskMib)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!next) return
    setError("")
    try {
      await save.mutateAsync({ path: { host: host.id }, body: next })
      invalidate(usePlatformCloudHostsList)
      toast.success("Allotment saved")
      onClose()
    } catch (err) {
      setError(errorOf(err, "The allotment wasn't saved."))
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
            <Button type="submit" disabled={save.isPending || !next}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
