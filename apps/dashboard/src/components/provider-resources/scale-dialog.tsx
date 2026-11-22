import { useQuery } from "@/client/hooks"
import { useState } from "react"
import type {
  Manifest,
  Provider,
  Revision,
  Connection,
} from "@/client/forge/loomplane/types"
import {
  useBindingsList,
  useManifestsEdit as edit,
  useManifestsSave as save,
  usePlansCreate as plan,
} from "@/client/forge/loomplane/hooks"
import { messageOf } from "@/client/errors"
import { useNavigate } from "@/lib/router"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
  DialogHeader,
  DialogFooter,
} from "@/components/ui/dialog"
import { useResourceFields, ResourceFields } from "./capacity-fields"

export function ScaleResourceDialog({
  projectId,
  name,
  env,
  revision,
  manifest,
  provider,
  account,
  region,
  close,
  saved,
}: {
  projectId: string
  name: string
  env: string
  revision: number
  manifest: Manifest
  provider: Provider
  account?: Connection
  region: string
  close: () => void
  saved: () => void
}) {
  const bindings = useQuery(useBindingsList, {
    path: { id: projectId },
    query: { environment: env },
  })
  const binding = bindings.data?.bindings.find(
    (b) =>
      b.name === name &&
      b.subject === "resource" &&
      b.provider === provider.name
  )
  const entry = manifest.resources?.[name]
  const initial = {
    ...entry?.settings,
    ...entry?.environments?.[env]?.settings,
  }
  const form = useResourceFields(
    provider.name,
    entry?.kind,
    binding?.connectionId || account?.id,
    binding?.region ||
      region ||
      account?.details?.region ||
      provider.regions?.[0] ||
      "",
    provider.provisions
      ?.find((p) => p.kind === entry?.kind)
      ?.settings?.filter((f) => f.capacity) ?? [],
    initial,
    entry?.version,
    binding?.externalId?.split("/").at(-1)
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [record, setRecord] = useState<Revision>()
  const navigate = useNavigate()
  const changed = form.fields.some(
    (f) => form.values[f.name] !== (initial[f.name] ?? f.default ?? "")
  )
  async function review() {
    if (
      busy ||
      !form.valid ||
      !entry ||
      entry.existingResource ||
      entry.existing_resource
    )
      return
    setBusy(true)
    setError("")
    try {
      let rev = record
      if (!rev) {
        const preview = await edit({
          path: { id: projectId },
          body: {
            dryRun: true,
            updateResource: {
              resource: name,
              environment: env,
              settings: form.values,
            },
          },
        })
        rev = await save({
          path: { id: projectId },
          body: { baseRevision: revision, yaml: preview.yaml },
        })
        setRecord(rev)
        saved()
      }
      const catalog = [
        manifest.services,
        manifest.gateways,
        manifest.links,
      ].every((m) => !Object.keys(m ?? {}).length)
      const result = await plan({
        path: { id: projectId },
        body: {
          environment: env,
          ...(catalog ? { catalog: true } : {}),
        },
      })
      close()
      void navigate({ to: "/deployments/$id", params: { id: result.id } })
    } catch (e) {
      setError(messageOf(e, "Could not review this capacity change."))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !busy) close()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Scale {name}</DialogTitle>
          <DialogDescription>
            {provider.title} · {env}. Review the capacity change before
            approving it.
          </DialogDescription>
        </DialogHeader>
        {bindings.status === "error" ? (
          <div role="alert">
            {messageOf(bindings.error, "Could not load the resource binding.")}{" "}
            <Button variant="outline" onClick={() => void bindings.refetch()}>
              Retry
            </Button>
          </div>
        ) : bindings.data ? (
          <ResourceFields form={form} disabled={busy || !!record} />
        ) : (
          <p role="status">Loading resource…</p>
        )}
        {error && <p role="alert">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={busy}>
            Cancel
          </Button>
          <Button
            onClick={() => void review()}
            disabled={
              busy ||
              (!record && (!changed || !form.edited)) ||
              !bindings.data ||
              bindings.status === "error" ||
              !form.valid
            }
          >
            {busy ? "Preparing plan…" : "Review deployment plan"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
