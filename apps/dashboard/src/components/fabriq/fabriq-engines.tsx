import { useEffect, useRef, useState } from "react"
import { messageOf } from "@/client/errors"
import {
  useFabriqRebuildsCreate,
  useFabriqRebuildsList,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { FabriqError } from "./fabriq-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { dependencyChoices, type CatalogPanelProps } from "@/lib/catalog-kinds"
import { useOrganization } from "@/lib/organization"

export const engineNames = {
  postgres: "Postgres",
  redis: "Redis",
  falkordb: "FalkorDB",
  elasticsearch: "Elasticsearch",
} as const

export function FabriqEngines({
  projectId,
  environmentId,
  resource,
  config,
}: CatalogPanelProps) {
  const { can } = useOrganization()
  const path = { projectId, environmentId, resource }
  const jobs = useQuery(useFabriqRebuildsList, { path })
  const create = useMutation(useFabriqRebuildsCreate)
  const [error, setError] = useState("")
  const requests = useRef<Record<string, string>>({})
  const submitting = useRef(false)
  const choices = Object.fromEntries(dependencyChoices(config))
  const active =
    jobs.data?.rebuilds.some((j) => ["queued", "running"].includes(j.status)) ??
    false
  const { refetch } = jobs
  useEffect(() => {
    if (!active) return
    const id = window.setInterval(() => {
      void refetch().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(id)
  }, [active, refetch])

  async function rebuild(projection: string) {
    if (submitting.current) return
    submitting.current = true
    setError("")
    requests.current[projection] ??= crypto.randomUUID()
    try {
      await create.mutateAsync({
        path,
        body: { projection, requestId: requests.current[projection] },
      })
      delete requests.current[projection]
      await jobs.refetch()
    } catch (cause) {
      setError(messageOf(cause, "The rebuild couldn't start. Try again."))
    } finally {
      submitting.current = false
    }
  }

  return (
    <div className="grid gap-3">
      {jobs.status === "error" && (
        <FabriqError
          error={jobs.error}
          fallback="Rebuild history couldn't be loaded."
          retry={jobs.refetch}
        />
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {Object.entries(engineNames).map(([id, name]) => {
        const choice = choices[id]
        const enabled = !!choice && choice.mode !== "off"
        const projection =
          id === "falkordb"
            ? "graph"
            : id === "elasticsearch"
              ? "search"
              : undefined
        const history =
          jobs.data?.rebuilds.filter((j) => j.projection === projection) ?? []
        const last = [...history].sort((a, b) =>
          b.createdAt.localeCompare(a.createdAt)
        )[0]
        const busy = history.some((j) =>
          ["queued", "running"].includes(j.status)
        )
        const origin = !enabled
          ? "Off"
          : choice.mode === "external"
            ? `External endpoint: ${choice.external}`
            : choice.mode === "resource"
              ? `Project resource: ${choice.resource}`
              : `Provisioned by Loomplane: ${choice.resource}`
        return (
          <section
            key={id}
            aria-label={name}
            className="min-w-0 rounded-xl border bg-card p-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <h2 className="text-sm font-semibold">{name}</h2>
                <p className="text-xs break-all text-muted-foreground">
                  {origin}
                </p>
              </div>
              {projection && enabled && can("deployment:create") && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={
                    !jobs.data ||
                    jobs.status === "error" ||
                    create.isPending ||
                    busy ||
                    (last?.status === "failed" && !last.retryable)
                  }
                  onClick={() => void rebuild(projection)}
                >
                  {last?.status === "failed" && last.retryable
                    ? "Retry rebuild"
                    : "Rebuild from Postgres"}
                </Button>
              )}
            </div>
            {projection && (
              <p className="mt-2 text-xs text-muted-foreground">
                Rebuilt from Postgres. No separate backups.
              </p>
            )}
            {last && (
              <div className="mt-2 grid gap-1 text-xs">
                <span role="status">{last.status}</span>
                {last.output && (
                  <pre className="max-h-44 overflow-auto rounded bg-subtle p-2 break-all whitespace-pre-wrap">
                    {last.output}
                  </pre>
                )}
              </div>
            )}
          </section>
        )
      })}
    </div>
  )
}
