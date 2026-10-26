import { useRef, useState } from "react"
import { messageOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useManifestsCurrent,
  useManifestsEdit,
  usePlansCreate,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { dependencyChoices, type CatalogPanelProps } from "@/lib/catalog-kinds"
import { isRecord } from "@/lib/service-recovery"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"

const projections = {
  falkordb: "FalkorDB",
  elasticsearch: "Elasticsearch",
} as const
export function FabriqSettings({
  projectId,
  environmentId,
  resource,
  config,
}: CatalogPanelProps) {
  const { can } = useOrganization()
  const allowed = can("deployment:create") && can("project:update")
  const navigate = useNavigate()
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const manifest = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const edit = useMutation(useManifestsEdit)
  const plan = useMutation(usePlansCreate)
  const [changes, setChanges] = useState<Record<string, boolean>>({})
  const [error, setError] = useState("")
  const submitting = useRef(false)
  const environment = environments.data?.environments.find(
    (e) => e.id === environmentId
  )?.name
  const choices = Object.fromEntries(dependencyChoices(config))
  const resourceConfig = manifest.data?.manifest?.resources?.[resource]
  const uses =
    isRecord(resourceConfig) && isRecord(resourceConfig.uses)
      ? resourceConfig.uses
      : undefined
  const current = (id: string) => {
    const use = uses?.[id]
    if (isRecord(use) && typeof use.enabled === "boolean") return use.enabled
    return !!choices[id] && choices[id].mode !== "off"
  }
  const busy = edit.isPending || plan.isPending
  async function review() {
    if (submitting.current || !environment || !allowed) return
    submitting.current = true
    setError("")
    try {
      for (const [dependency, enabled] of Object.entries(changes))
        await edit.mutateAsync({
          path: { id: projectId },
          body: { setUse: { resource, dependency, use: { enabled } } },
        })
      const doc = manifest.data?.manifest
      const catalog =
        doc?.version === 1 &&
        [doc.services, doc.gateways, doc.links].every(
          (section) => !section || Object.keys(section).length === 0
        )
      const result = await plan.mutateAsync({
        path: { id: projectId },
        body: { environment, ...(catalog ? { catalog: true } : {}) },
      })
      await navigate({ to: "/deployments/$id", params: { id: result.id } })
    } catch (cause) {
      setError(
        `The manifest may already contain your changes. Review again to retry planning. ${messageOf(cause, "The plan couldn't start.")}`
      )
    } finally {
      submitting.current = false
    }
  }
  return (
    <section aria-label="Optional projections" className="grid gap-3">
      <p className="text-[12.5px] text-muted-foreground">
        Choose the projections you need. Changes take effect after you approve
        and deploy the plan.
      </p>
      {manifest.status === "error" && (
        <QueryError
          error={manifest.error}
          fallback="Couldn't load the manifest."
          retry={manifest.refetch}
        />
      )}
      {environments.status === "error" && (
        <QueryError
          error={environments.error}
          fallback="Couldn't load environments."
          retry={environments.refetch}
        />
      )}
      {Object.entries(projections).map(([id, title]) => (
        <label
          key={id}
          className="flex items-center justify-between gap-4 rounded-xl bg-card px-4 py-3.5 ring-1 ring-border"
        >
          <span className="grid gap-0.5">
            <span className="text-[14px] font-semibold">{title}</span>
            <span className="text-[12.5px] text-muted-foreground">
              {id === "falkordb" ? "Graph" : "Search"} projections. Rebuilt from
              Postgres, so no backups.
            </span>
          </span>
          <Switch
            aria-label={title}
            checked={changes[id] ?? current(id)}
            disabled={busy || !allowed}
            onCheckedChange={(enabled) =>
              setChanges((previous) => ({ ...previous, [id]: enabled }))
            }
          />
        </label>
      ))}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          Nothing changes on the target until you deploy.
        </span>
        <Button
          disabled={
            busy ||
            !environment ||
            !manifest.data ||
            manifest.status === "error" ||
            !Object.keys(changes).length ||
            !allowed
          }
          onClick={() => void review()}
        >
          Review plan
        </Button>
      </div>
    </section>
  )
}
