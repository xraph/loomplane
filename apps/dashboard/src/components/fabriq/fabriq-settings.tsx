import { useId, useRef, useState } from "react"
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
  falkordb: {
    title: "FalkorDB",
    kind: "Graph",
    removes: "its data",
    queries: "Graph queries",
  },
  elasticsearch: {
    title: "Elasticsearch",
    kind: "Search",
    removes: "its search index",
    queries: "Search queries",
  },
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
  // Set when planning failed after the manifest edits were written. The
  // manifest then holds the change, so a refetch leaves nothing pending, and
  // the next review only has to plan.
  const [planOnly, setPlanOnly] = useState(false)
  const submitting = useRef(false)
  const descriptions = useId()
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
  // A switch flipped and flipped back is not a change.
  const pending = Object.entries(changes).filter(
    ([id, enabled]) => enabled !== current(id)
  )
  const busy = edit.isPending || plan.isPending
  async function review() {
    if (submitting.current || !environment || !allowed) return
    submitting.current = true
    setError("")
    let written = planOnly
    try {
      for (const [dependency, enabled] of pending) {
        await edit.mutateAsync({
          path: { id: projectId },
          body: { setUse: { resource, dependency, use: { enabled } } },
        })
        written = true
      }
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
      setPlanOnly(false)
      await navigate({ to: "/deployments/$id", params: { id: result.id } })
    } catch (cause) {
      setPlanOnly(written)
      setError(
        `The manifest may already contain your changes. Review again to retry planning. ${messageOf(cause, "The plan couldn't start.")}`
      )
    } finally {
      submitting.current = false
    }
  }
  return (
    <section aria-label="Optional projections" className="grid gap-3">
      <p className="text-[12.5px] leading-[1.5] text-muted-foreground">
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
      {Object.entries(projections).map(([id, projection]) => {
        const { title } = projection
        const checked = changes[id] ?? current(id)
        const warn = !checked && current(id)
        const about = `${descriptions}-${id}-about`
        const warning = `${descriptions}-${id}-warning`
        return (
          <div
            key={id}
            className="grid gap-2 rounded-xl border border-border-panel bg-card px-3.5 py-3"
          >
            <label className="flex items-center justify-between gap-4">
              <span>
                <span className="text-[15px] leading-[normal] font-semibold">
                  {title}
                </span>
                <span
                  id={about}
                  className="block text-[12.5px] leading-[1.5] text-muted-foreground"
                >
                  {projection.kind} projections. Rebuilt from Postgres, so no
                  backups.
                </span>
              </span>
              <Switch
                className="data-[size=default]:h-5"
                aria-label={title}
                aria-describedby={warn ? `${about} ${warning}` : about}
                checked={checked}
                disabled={busy || !allowed}
                onCheckedChange={(enabled) =>
                  setChanges((previous) => ({ ...previous, [id]: enabled }))
                }
              />
            </label>
            {warn && (
              <p
                id={warning}
                role="note"
                className="rounded-[9px] bg-notice-warning px-2.5 py-2 text-[12.5px] leading-normal text-notice-warning-ink"
              >
                Turning {title} off removes it and {projection.removes}.{" "}
                {projection.queries} fail until you turn it back on.
              </p>
            )}
          </div>
        )
      })}
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
          className="h-8 rounded-[9px] px-3 text-[12.5px]"
          disabled={
            busy ||
            !environment ||
            !manifest.data ||
            manifest.status === "error" ||
            !(pending.length || planOnly) ||
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
