// The Services section: which services use Fabriq, the key each holds, and
// the tenant it defaults to. Rotate and revoke act on one service's key
// and leave the others working.
import { FabriqEmpty } from "./fabriq-empty"
import { PlusIcon } from "lucide-react"
import { useId, useState, type FormEvent } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useFabriqKeysList,
  useFabriqKeysRevoke,
  useFabriqKeysRotate,
  useFabriqTenantsList,
  useManifestsCurrent,
  useManifestsEdit,
} from "@/client/forge/loomplane/hooks"
import type { ConsumerKey } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { FabriqError } from "@/components/fabriq/fabriq-error"
import { StatusPill } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { ago, unlinkedServices } from "@/lib/fabriq"
import { useOrganization } from "@/lib/organization"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** FabriqServices lists the services using Fabriq and manages their keys. */
export function FabriqServices({
  projectId,
  environmentId,
  resource,
}: CatalogPanelProps) {
  const path = { projectId, environmentId, resource }
  const { can } = useOrganization()
  const now = useNow(30_000)
  const keys = useQuery(useFabriqKeysList, { path })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const rotate = useMutation(useFabriqKeysRotate)
  const revoke = useMutation(useFabriqKeysRevoke)
  const [linking, setLinking] = useState(false)
  const [revoking, setRevoking] = useState<string>()
  const [busy, setBusy] = useState<string>()
  const environment = environments.data?.environments.find(
    (e) => e.id === environmentId
  )?.name
  const canManage = can("secret:manage")

  const rotateKey = async (service: string) => {
    setBusy(service)
    try {
      await rotate.mutateAsync({ path: { ...path, service } })
    } catch (cause) {
      toast.error(messageOf(cause, `${service}'s key wasn't rotated.`))
    } finally {
      setBusy(undefined)
    }
  }

  const revokeKey = async (service: string) => {
    setBusy(service)
    try {
      await revoke.mutateAsync({ path: { ...path, service } })
      setRevoking(undefined)
    } catch (cause) {
      toast.error(messageOf(cause, `${service}'s key wasn't revoked.`))
    } finally {
      setBusy(undefined)
    }
  }

  return (
    <section aria-label="Services using Fabriq" className="grid gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="grid gap-0.5">
          <h2 className="text-[15px] font-semibold">Services using Fabriq</h2>
          <span className="text-[12.5px] leading-normal text-muted-foreground">
            Each service gets its own key in{" "}
            <span className="font-mono text-xs">FABRIQ_URL</span>, so revoking
            one leaves the others working.
          </span>
        </div>
        {keys.status !== "error" &&
          !!keys.data?.keys.length &&
          can("project:update") && (
            <Button
              type="button"
              size="sm"
              aria-expanded={linking}
              onClick={() => setLinking(!linking)}
            >
              <PlusIcon data-icon="inline-start" aria-hidden />
              Link a service
            </Button>
          )}
      </div>
      {linking && (
        <LinkService
          projectId={projectId}
          environmentId={environmentId}
          resource={resource}
          onLinked={() => setLinking(false)}
        />
      )}
      {keys.status === "error" ? (
        <FabriqError
          error={keys.error}
          fallback="Fabriq's keys couldn't be loaded."
          retry={keys.refetch}
        />
      ) : !keys.data ? (
        <Skeleton
          role="status"
          aria-label="Loading services"
          className="h-24 rounded-xl"
        />
      ) : keys.data.keys.length === 0 ? (
        <FabriqEmpty
          title="No services linked"
          description="Link a service and its next release gets its own Fabriq key."
        >
          {can("project:update") && (
            <Button size="sm" onClick={() => setLinking(true)}>
              Link a service
            </Button>
          )}
        </FabriqEmpty>
      ) : (
        <Card className="py-0">
          <Table className="min-w-[760px]">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Service</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Tenant</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.data.keys.map((key) => (
                <TableRow key={key.service}>
                  <TableCell>
                    <span className="grid gap-0.5">
                      <span className="font-medium">{key.service}</span>
                      {environment && (
                        <span className="text-xs text-muted-foreground">
                          {environment}
                        </span>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <KeyCell value={key} />
                  </TableCell>
                  <TableCell className="font-mono text-[12.5px]">
                    {key.tenant}
                  </TableCell>
                  <TableCell className="text-[12.5px] text-muted-foreground">
                    {lastUsed(key, now)}
                  </TableCell>
                  <TableCell>
                    {canManage &&
                      (key.state === "active" || key.state === "rotating") && (
                        <span className="flex justify-end gap-1.5">
                          {key.state === "active" && (
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              disabled={busy === key.service}
                              onClick={() => void rotateKey(key.service)}
                            >
                              Rotate
                            </Button>
                          )}
                          <Button
                            type="button"
                            variant="destructive"
                            size="sm"
                            disabled={busy === key.service}
                            onClick={() => setRevoking(key.service)}
                          >
                            Revoke
                          </Button>
                        </span>
                      )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <span className="text-xs text-muted-foreground">
        Loomplane keeps its own operator key to manage tenants and keys. It is
        never shown or given to a service.
      </span>
      <AlertDialog
        open={!!revoking}
        onOpenChange={(open) => {
          if (!open && !revoke.isPending) setRevoking(undefined)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke {revoking}&apos;s key?</AlertDialogTitle>
            <AlertDialogDescription>
              {revoking} loses access to Fabriq now. Its next release gets a new
              key unless you remove fabriq from its needs.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revoke.isPending}>
              Cancel
            </AlertDialogCancel>
            <Button
              type="button"
              variant="destructive"
              disabled={revoke.isPending}
              onClick={() => revoking && void revokeKey(revoking)}
            >
              Revoke key
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

// The key a service holds: the start of it and where it stands. A service
// that needs Fabriq but hasn't been released yet has none to show.
function KeyCell({ value }: { value: ConsumerKey }) {
  if (value.state === "unreleased")
    return (
      <span className="text-[12.5px] text-muted-foreground">
        Issued on its next release
      </span>
    )
  return (
    <span className="flex items-center gap-2">
      {value.prefix && (
        <span className="font-mono text-[12.5px]">{value.prefix}…</span>
      )}
      {value.state === "active" && <StatusPill tone="live">Active</StatusPill>}
      {value.state === "rotating" && (
        <span title="The new key takes effect on the service's next release.">
          <StatusPill tone="waiting">Rotating</StatusPill>
        </span>
      )}
      {value.state === "revoked" && (
        <StatusPill tone="quiet">Revoked</StatusPill>
      )}
    </span>
  )
}

// Fabriq may not report when a key was used: without it a key with no use
// may have been used, and says so rather than Never.
function lastUsed(key: ConsumerKey, now: number): string {
  if (key.state === "revoked")
    return key.revokedAt ? `Revoked ${ago(key.revokedAt, now)}` : "Revoked"
  if (key.lastUsedAt) return ago(key.lastUsedAt, now)
  return key.lastUsedKnown ? "Never" : "Not reported"
}

/**
 * LinkService makes a service of the project use Fabriq, with a default
 * tenant. It edits the manifest and plans nothing: the service gets its key
 * on its next release.
 */
function LinkService({
  projectId,
  environmentId,
  resource,
  onLinked,
}: {
  projectId: string
  environmentId: string
  resource: string
  onLinked: () => void
}) {
  const invalidate = useInvalidate()
  const manifest = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const tenants = useQuery(useFabriqTenantsList, {
    path: { projectId, environmentId, resource },
  })
  const edit = useMutation(useManifestsEdit)
  const [service, setService] = useState("")
  const [tenant, setTenant] = useState("")
  const [error, setError] = useState("")

  const services = unlinkedServices(manifest.data?.manifest?.services, resource)
  const tenantIds = (tenants.data?.tenants ?? []).map((t) => t.id)
  const chosenService = services.includes(service) ? service : services[0]
  const chosenTenant = tenantIds.includes(tenant) ? tenant : tenantIds[0]

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!chosenService || !chosenTenant || edit.isPending) return
    setError("")
    try {
      await edit.mutateAsync({
        path: { id: projectId },
        body: {
          link: {
            service: chosenService,
            need: resource,
            options: { tenant: chosenTenant },
          },
        },
      })
      invalidate(useManifestsCurrent)
      invalidate(useFabriqKeysList)
      onLinked()
    } catch (cause) {
      setError(messageOf(cause, `${chosenService} wasn't linked.`))
    }
  }

  return (
    <form
      onSubmit={(event) => void submit(event)}
      className="flex flex-wrap items-end gap-2.5 rounded-xl bg-subtle p-3 ring-1 ring-border"
    >
      <Pick
        label="Service"
        value={chosenService ?? ""}
        items={services}
        empty="No service to link"
        onChange={setService}
      />
      <Pick
        label="Default tenant"
        value={chosenTenant ?? ""}
        items={tenantIds}
        mono
        empty="No tenant"
        onChange={setTenant}
      />
      <span className="flex-[1_1_220px] text-xs text-muted-foreground">
        Takes effect on the service&apos;s next release. The app can still pick
        another tenant per request.
      </span>
      <Button
        type="submit"
        disabled={!chosenService || !chosenTenant || edit.isPending}
      >
        Link service
      </Button>
      {error && (
        <Alert variant="destructive" className="basis-full">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}

function Pick({
  label,
  value,
  items,
  mono,
  empty,
  onChange,
}: {
  label: string
  value: string
  items: string[]
  mono?: boolean
  empty: string
  onChange: (value: string) => void
}) {
  const id = useId()
  return (
    <div className="grid flex-[1_1_180px] gap-1.5">
      <span id={id} className="text-[12.5px] font-medium">
        {label}
      </span>
      <Select
        items={items.map((item) => ({ label: item, value: item }))}
        value={value || null}
        onValueChange={(next) => onChange(String(next))}
        disabled={items.length === 0}
      >
        <SelectTrigger
          aria-labelledby={id}
          className={cn("w-full bg-background", mono && "font-mono")}
        >
          <SelectValue placeholder={empty} />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem
              key={item}
              value={item}
              className={cn(mono && "font-mono")}
            >
              {item}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
