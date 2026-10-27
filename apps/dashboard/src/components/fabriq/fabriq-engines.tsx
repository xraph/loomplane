import { useEffect, useRef, useState } from "react"
import { messageOf } from "@/client/errors"
import {
  useFabriqRebuildsCreate,
  useFabriqRebuildsList,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { ago } from "@/lib/fabriq"
import { stackTones } from "./fabriq-stack"
import { useFabriqStack } from "./use-fabriq-stack"
import { FabriqError } from "./fabriq-error"
import { Panel } from "@/components/panel"
import { StatusPill } from "@/components/runs"
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

// The words a rebuild's state goes by on the page, not the API's names.
const rebuildStates: Record<string, string> = {
  queued: "queued",
  running: "running",
  succeeded: "completed",
  failed: "failed",
}

export function FabriqEngines(props: CatalogPanelProps) {
  const { projectId, environmentId, resource, config } = props
  const { parts, now } = useFabriqStack(props)
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
        const part = parts.find((part) => part.id === id)!
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
          <Panel key={id} aria-label={name} className="gap-2">
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
              <div>
                <h2 className="text-[14px] font-semibold">{part.name}</h2>
                <p className="text-[12.5px] break-all text-muted-foreground">
                  {origin}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                <StatusPill tone={stackTones[part.state]}>
                  {part.state}
                </StatusPill>
                {projection && enabled && can("deployment:create") && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={
                      !jobs.data ||
                      jobs.status === "error" ||
                      create.isPending ||
                      busy
                    }
                    onClick={() => void rebuild(projection)}
                  >
                    {last?.status === "failed" && last.retryable
                      ? "Retry rebuild"
                      : "Rebuild from Postgres"}
                  </Button>
                )}
              </div>
            </div>
            <p className="text-xs text-muted-foreground">
              {projection && enabled
                ? "Rebuilt from Postgres. No separate backups."
                : id === "postgres"
                  ? part.detail.replace(
                      "TimescaleDB, pgvector, PostGIS.",
                      "TimescaleDB, pgvector and PostGIS."
                    )
                  : part.detail}
            </p>
            {last && (
              <div className="grid gap-2 border-t pt-2 text-xs text-muted-foreground">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span>
                    Last rebuild:{" "}
                    <span role="status">
                      {rebuildStates[last.status] ?? last.status}
                    </span>
                  </span>
                  <time dateTime={last.createdAt}>
                    {ago(last.createdAt, now)}
                  </time>
                </div>
                {last.output && (
                  <pre className="max-h-44 overflow-auto rounded-lg bg-subtle p-2 font-mono break-all whitespace-pre-wrap">
                    {last.output}
                  </pre>
                )}
              </div>
            )}
          </Panel>
        )
      })}
    </div>
  )
}
