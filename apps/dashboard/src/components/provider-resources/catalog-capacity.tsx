import { useState } from "react"

import {
  useManifestsCurrent,
  useProvidersList,
  useProjectsPermissions,
} from "@/client/forge/loomplane/hooks"
import type { Binding, Provider } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { placementSettings } from "@/lib/placement"
import { bindingConfig, isRecord } from "@/lib/service-recovery"

import { ScaleResourceDialog } from "./scale-dialog"

type Props = {
  binding: Binding
  kind: string
  projectId: string
  environment: string
}

export function CatalogCapacity(props: Props) {
  const { binding, kind, projectId } = props
  const catalog = useQuery(useProvidersList)
  const permissions = useQuery(useProjectsPermissions, {
    path: { id: projectId },
  })
  const grants =
    permissions.status === "success" ? permissions.data?.permissions : undefined
  const config = bindingConfig(binding)
  if (config?.existing_resource || config?.existingResource) return null
  if (catalog.status === "error")
    return (
      <QueryError
        error={catalog.error}
        retry={catalog.refetch}
        fallback="Capacity options could not be loaded."
      />
    )
  const provider = catalog.data?.providers?.find(
    (p) => p.name === binding.provider
  )
  const fields = provider?.provisions
    ?.find((o) => o.kind === kind)
    ?.settings?.filter((f) => f.capacity)
  if (!provider || !fields?.length) return null
  const settings = isRecord(config?.settings) ? config.settings : {}
  return (
    <section aria-label="Capacity" className="grid min-w-0 gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[15px] font-semibold">Capacity</h2>
        {provider.available &&
          binding.drift !== "missing" &&
          grants?.includes("project:update") &&
          grants.includes("deployment:create") && (
            <ScaleAction {...props} provider={provider} />
          )}
      </div>
      <dl
        aria-label="Deployed capacity settings"
        className="flex flex-wrap gap-x-8 gap-y-2 rounded-xl bg-card px-4 py-3 ring-1 ring-border"
      >
        {fields.map((field) => (
          <div className="min-w-0" key={field.name}>
            <dt className="text-xs text-muted-foreground">{field.title}</dt>
            <dd className="text-[13px] font-medium break-words">
              {typeof settings[field.name] === "string"
                ? String(settings[field.name])
                : "Provider default"}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function ScaleAction({
  binding,
  kind,
  projectId,
  environment,
  provider,
}: Props & { provider: Provider }) {
  const [open, setOpen] = useState(false)
  const current = useQuery(useManifestsCurrent, { path: { id: projectId } })
  if (current.status === "error")
    return (
      <QueryError
        error={current.error}
        retry={current.refetch}
        fallback="The resource configuration could not be loaded."
      />
    )
  const revision = current.data
  const manifest = revision?.manifest
  const entry = manifest?.resources?.[binding.name]
  if (
    !revision ||
    !manifest ||
    !entry ||
    entry.kind !== kind ||
    entry.existingResource ||
    entry.existing_resource
  )
    return null
  const placement = placementSettings(
    manifest,
    { section: "resources", name: binding.name, kind, entry },
    environment
  )
  if (
    !placement.enabled ||
    (placement.provider && placement.provider !== provider.name) ||
    (placement.target && placement.target !== binding.connectionId) ||
    (placement.region && binding.region && placement.region !== binding.region)
  )
    return null
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        Scale
      </Button>
      {open && (
        <ScaleResourceDialog
          projectId={projectId}
          env={environment}
          name={binding.name}
          provider={provider}
          region={binding.region}
          manifest={manifest}
          revision={revision.revision}
          saved={() => void current.refetch()}
          close={() => setOpen(false)}
        />
      )}
    </>
  )
}
