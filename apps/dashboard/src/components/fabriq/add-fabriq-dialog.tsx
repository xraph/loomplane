// The Add Fabriq dialog: one click sets up the whole stack, and any engine
// can be swapped for one the project already runs. Nothing is created
// here. Submitting adds the resource to the project's manifest and plans
// the environment, and the plan opens for review like any other.
import { Radio } from "@base-ui/react/radio"
import { RadioGroup } from "@base-ui/react/radio-group"
import { useId, useState, type FormEvent, type ReactNode } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useCatalogEndpointsCheck,
  useCatalogEndpointsCreate,
  useCatalogEndpointsList,
  useConnectionsList,
  useEnvironmentsList,
  useManifestsCurrent,
} from "@/client/forge/loomplane/hooks"
import type { CheckResult } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { FabriqIcon } from "./fabriq-icon"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
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
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  addCatalogService,
  freeName,
  takenNames,
  targetsFor,
} from "@/lib/catalog-add"
import type { CatalogAddProps } from "@/lib/catalog-kinds"
import {
  checkMessage,
  fabriqKindFor,
  fabriqSummary,
  fabriqUses,
  type CheckEngine,
  type FabriqChoice,
  type FabriqLink,
  type FabriqMode,
} from "@/lib/fabriq"
import { cn } from "@/lib/utils"

const modes: { id: FabriqMode; label: string; description: string }[] = [
  {
    id: "server",
    label: "Hosted server",
    description:
      "Apps in any language connect with a fabriq:// key. Includes the admin console.",
  },
  {
    id: "library",
    label: "Library only",
    description:
      "Your Go app embeds Fabriq. Loomplane runs the engines, migrations and worker.",
  },
]

type EngineId = keyof Omit<FabriqChoice, "mode">

type Engine = {
  id: EngineId
  name: string
  need: "required" | "optional"
  detail: string
  options: { value: string; label: string }[]
} & (
  | {
      check: CheckEngine
      /** The dependency in the Fabriq kind this engine fills. */
      dependency: string
      /** Manifest kinds a project's own resource can fill it with. */
      kinds: string[]
      dsnLabel: string
      dsnHint: string
    }
  | { check?: undefined }
)

const provision = { value: "new", label: "Provision new" }
const connect = { value: "existing", label: "Connect existing" }
const off = { value: "off", label: "Off" }

const engines: Engine[] = [
  {
    id: "pg",
    name: "Postgres 16",
    need: "required",
    detail:
      "With TimescaleDB, pgvector and PostGIS. Daily backups when Loomplane runs it.",
    options: [provision, connect],
    check: "postgres",
    dependency: "postgres",
    kinds: ["timescaledb"],
    dsnLabel: "Postgres connection string",
    dsnHint: "postgres://user@db.internal:5432/fabriq",
  },
  {
    id: "redis",
    name: "Redis",
    need: "required",
    detail: "Carries the outbox stream and live subscriptions.",
    options: [provision, connect],
    check: "redis",
    dependency: "redis",
    kinds: ["redis", "valkey"],
    dsnLabel: "Redis address",
    dsnHint: "redis://cache.internal:6379",
  },
  {
    id: "falkor",
    name: "FalkorDB",
    need: "optional",
    detail: "Graph projections. Rebuilt from Postgres, so no backups.",
    options: [off, provision],
  },
  {
    id: "es",
    name: "Elasticsearch",
    need: "optional",
    detail: "Search projections. Rebuilt from Postgres, so no backups.",
    options: [off, provision],
  },
]

/** What the dialog keeps for an engine that connects to something existing. */
type Existing = {
  /** A resource in this project, or undefined for a connection string. */
  resource?: string
  /** Kept only until it is sent, and never put in a URL. */
  dsn: string
  check?: { target: string; result: CheckResult }
  /** The external endpoint saved from the connection string. */
  endpoint?: string
}

const noExisting: Existing = { dsn: "" }

/**
 * AddFabriqDialog adds Fabriq to a project, as a hosted server or as the
 * library an app embeds, with each engine provisioned new or connected to
 * one that already exists.
 */
export function AddFabriqDialog({
  projectId,
  projectName,
  onClose,
  onPlanned,
}: CatalogAddProps) {
  const ids = useId()
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const current = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const connections = useQuery(useConnectionsList)
  const checking = useMutation(useCatalogEndpointsCheck)
  const saving = useMutation(useCatalogEndpointsCreate)
  const invalidate = useInvalidate()

  const [choice, setChoice] = useState<FabriqChoice>({
    mode: "server",
    pg: "new",
    redis: "new",
    falkor: "off",
    es: "off",
  })
  const [environment, setEnvironment] = useState("")
  const [target, setTarget] = useState("")
  const [existing, setExisting] = useState<Record<"pg" | "redis", Existing>>({
    pg: noExisting,
    redis: noExisting,
  })
  const [checkingEngine, setCheckingEngine] = useState<"pg" | "redis">()
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState("")
  const [error, setError] = useState("")

  const environmentList = environments.data?.environments ?? []
  const environmentName =
    environmentList.find((e) => e.name === environment)?.name ??
    (environmentList.find((e) => e.name === "production") ?? environmentList[0])
      ?.name
  const targets = targetsFor(
    { targets: ["server", "kubernetes"] },
    connections.data?.connections ?? []
  )
  const selected = targets.find((t) => t.connection.id === target) ?? targets[0]
  const manifest = current.data?.manifest
  const name = manifest ? freeName("fabriq", takenNames(manifest)) : ""

  const patch = (id: "pg" | "redis", change: Partial<Existing>) =>
    setExisting((all) => ({ ...all, [id]: { ...all[id], ...change } }))

  // Resources the project already has that an engine can be linked to, on
  // the chosen target.
  const resourcesFor = (engine: Engine) =>
    Object.entries(manifest?.resources ?? {})
      .filter(
        ([, r]) =>
          engine.check &&
          typeof r?.kind === "string" &&
          engine.kinds.includes(r.kind) &&
          !!selected &&
          r.target === selected.connection.id
      )
      .map(([resource]) => resource)

  // Where an engine that connects to something existing stands: a resource
  // in the project, a saved endpoint, or a connection string that passed.
  const linked = (engine: Engine & { check: CheckEngine }) => {
    const state = existing[engine.id as "pg" | "redis"]
    const resource = resourcesFor(engine).find((r) => r === state.resource)
    if (resource) return true
    return (
      !!state.endpoint ||
      (!!selected &&
        state.check?.target === selected.connection.id &&
        state.check.result.ok)
    )
  }

  const unfinished = engines.some(
    (e) => e.check && choice[e.id] === "existing" && !linked(e)
  )
  const loading =
    !environments.data || !current.data?.manifest || !connections.data
  const blocked =
    pending || loading || !selected || !environmentName || unfinished

  const check = async (engine: Engine & { check: CheckEngine }) => {
    const id = engine.id as "pg" | "redis"
    const { dsn } = existing[id]
    if (!selected || !dsn || checkingEngine) return
    const forTarget = selected.connection.id
    setCheckingEngine(id)
    let result: CheckResult
    try {
      // The generated mutation doesn't carry the response's type.
      result = (await checking.mutateAsync({
        body: {
          engine: engine.check,
          kind: fabriqKindFor(choice.mode),
          dependency: engine.dependency,
          target: forTarget,
          dsn,
        },
      })) as CheckResult
    } catch (cause) {
      result = {
        ok: false,
        problems: [messageOf(cause, "The connection couldn't be checked.")],
      }
    }
    setCheckingEngine(undefined)
    // A string edited while it was being checked isn't the one checked.
    setExisting((all) =>
      all[id].dsn === dsn
        ? { ...all, [id]: { ...all[id], check: { target: forTarget, result } } }
        : all
    )
  }

  const submit = async (event: FormEvent) => {
    // The dialog is portalled out of any form, but React still bubbles
    // this submit to the one around it.
    event.preventDefault()
    event.stopPropagation()
    if (blocked || !selected || !environmentName || !name) return
    setPending(true)
    setError("")
    try {
      const links: { postgres?: FabriqLink; redis?: FabriqLink } = {}
      for (const engine of engines) {
        if (!engine.check || choice[engine.id] !== "existing") continue
        const id = engine.id as "pg" | "redis"
        const state = existing[id]
        const resource = resourcesFor(engine).find((r) => r === state.resource)
        const key = engine.dependency as "postgres" | "redis"
        if (resource) {
          links[key] = { resource }
          continue
        }
        let endpoint = state.endpoint
        if (!endpoint) {
          setProgress(`Saving the ${engine.name} connection…`)
          const saved = await saving.mutateAsync({
            body: {
              name: `${name}-${engine.dependency}`,
              engine: engine.check,
              dsn: state.dsn,
            },
          })
          endpoint = saved.id
          invalidate(useCatalogEndpointsList)
          // Sent, so it isn't kept. A retry uses the saved endpoint.
          patch(id, { dsn: "", endpoint })
        }
        links[key] = { external: endpoint }
      }
      const { planId } = await addCatalogService(
        {
          projectId,
          environment: environmentName,
          name,
          kind: fabriqKindFor(choice.mode),
          provider: selected.provider,
          target: selected.connection.id,
          uses: fabriqUses(choice, links),
        },
        setProgress
      )
      onPlanned(planId)
    } catch (cause) {
      setError(messageOf(cause, "Fabriq wasn't added."))
      setProgress("")
    } finally {
      setPending(false)
    }
  }

  const locked = pending
  return (
    <Dialog open onOpenChange={(open) => !open && !pending && onClose()}>
      <DialogContent
        showCloseButton={false}
        className="flex flex-col gap-0 overflow-hidden p-0 sm:max-w-[640px]"
      >
        <form
          onSubmit={(event) => void submit(event)}
          className="flex min-h-0 flex-1 flex-col"
        >
          <DialogHeader className="flex-row items-center gap-3 px-5 pt-5 pb-1">
            <ProductIcon fallback={FabriqIcon} size="lg" />
            <div className="grid min-w-0 gap-1">
              <DialogTitle>Add Fabriq to {projectName}</DialogTitle>
              <DialogDescription>
                One click sets up the whole stack. Swap any engine for one you
                already run.
              </DialogDescription>
            </div>
          </DialogHeader>

          <div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-5 pt-3 pb-4">
            {environments.status === "error" ? (
              <QueryError
                error={environments.error}
                fallback="The project's environments couldn't be loaded."
                retry={environments.refetch}
              />
            ) : current.status === "error" ? (
              statusOf(current.error) === 404 ? (
                <Alert>
                  <AlertDescription>
                    {projectName} has no deployment configuration to add Fabriq
                    to. Open the project to save one first.
                  </AlertDescription>
                </Alert>
              ) : (
                <QueryError
                  error={current.error}
                  fallback="The project's deployment configuration couldn't be loaded."
                  retry={current.refetch}
                />
              )
            ) : connections.status === "error" ? (
              <QueryError
                error={connections.error}
                fallback="Targets couldn't be loaded."
                retry={connections.refetch}
              />
            ) : loading ? (
              <Skeleton
                role="status"
                aria-label="Loading the project"
                className="h-24"
              />
            ) : (
              <div className="grid gap-2.5 sm:grid-cols-2">
                <Pick
                  id={`${ids}-environment`}
                  label="Environment"
                  value={environmentName ?? ""}
                  items={environmentList.map((e) => ({
                    label: e.name,
                    value: e.name,
                  }))}
                  onChange={setEnvironment}
                  disabled={locked}
                />
                <Pick
                  id={`${ids}-target`}
                  label="Target"
                  value={selected?.connection.id ?? ""}
                  items={targets.map((t) => ({
                    label: t.connection.name,
                    value: t.connection.id,
                  }))}
                  onChange={setTarget}
                  disabled={locked}
                  empty="No checked target"
                />
                {!selected && (
                  <p className="text-xs text-muted-foreground sm:col-span-2">
                    Fabriq runs on a checked Docker or Kubernetes target. Add
                    and check one in Integrations first.
                  </p>
                )}
              </div>
            )}

            <RadioGroup
              value={choice.mode}
              onValueChange={(mode) => setChoice({ ...choice, mode })}
              disabled={locked}
              aria-label="How your apps use Fabriq"
              className="grid gap-2 sm:grid-cols-2"
            >
              {modes.map((mode) => {
                const on = choice.mode === mode.id
                return (
                  <Radio.Root
                    key={mode.id}
                    value={mode.id}
                    nativeButton
                    render={<button type="button" />}
                    className={cn(
                      "grid content-start gap-1 rounded-xl border px-3 py-2.5 text-left transition-shadow disabled:opacity-60",
                      on
                        ? "border-brand-line bg-brand-soft ring-3 ring-ring/35"
                        : "bg-card hover:bg-muted/50"
                    )}
                  >
                    <span className="text-[13px] font-semibold">
                      {mode.label}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {mode.description}
                    </span>
                  </Radio.Root>
                )
              })}
            </RadioGroup>

            <div className="grid gap-1.5">
              <span className="text-[12.5px] font-medium">Engines</span>
              <div className="divide-y overflow-hidden rounded-xl ring-1 ring-border">
                {engines.map((engine) => (
                  <div key={engine.id} className="px-3.5 py-3">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                      <span className="grid min-w-[220px] flex-[1_1_220px] gap-0.5">
                        <span className="text-[13px] font-semibold">
                          {engine.name}{" "}
                          <span className="text-xs font-normal text-muted-foreground">
                            {engine.need}
                          </span>
                        </span>
                        <span className="text-xs text-muted-foreground">
                          {engine.id === "pg" &&
                          selected?.connection.cloud === "kubernetes"
                            ? "With TimescaleDB, pgvector and PostGIS. Backups on Kubernetes aren’t available yet."
                            : engine.detail}
                        </span>
                      </span>
                      <Segments
                        label={engine.name}
                        value={choice[engine.id]}
                        options={engine.options}
                        disabled={locked}
                        onChange={(value) =>
                          setChoice({ ...choice, [engine.id]: value })
                        }
                      />
                    </div>
                    {engine.check && choice[engine.id] === "existing" && (
                      <Connecting
                        engine={engine}
                        state={existing[engine.id as "pg" | "redis"]}
                        resources={resourcesFor(engine)}
                        target={selected?.connection.id}
                        checking={checkingEngine === engine.id}
                        disabled={locked}
                        onChange={(change) =>
                          patch(engine.id as "pg" | "redis", change)
                        }
                        onCheck={() => void check(engine)}
                      />
                    )}
                  </div>
                ))}
              </div>
            </div>

            <div className="grid gap-1 rounded-lg bg-muted px-3 py-2.5">
              <span className="text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase">
                This creates
              </span>
              <span className="text-[13px] leading-relaxed">
                {fabriqSummary(choice)}
              </span>
            </div>

            {progress && !error && (
              <p role="status" className="text-[13px]">
                {progress}
              </p>
            )}
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter className="m-0 flex-wrap px-5 py-3 sm:items-center sm:justify-between">
            <span className="text-xs text-muted-foreground">
              Nothing is created until you approve the plan.
            </span>
            <span className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={pending}
                onClick={onClose}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={blocked}>
                {pending && <Spinner data-icon="inline-start" />}
                Review plan
              </Button>
            </span>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Pick is a labelled select, such as the environment or the target. */
function Pick({
  id,
  label,
  value,
  items,
  onChange,
  disabled,
  empty,
}: {
  id: string
  label: string
  value: string
  items: { label: string; value: string }[]
  onChange: (value: string) => void
  disabled: boolean
  empty?: string
}) {
  return (
    <div className="grid min-w-0 gap-1.5">
      <span id={id} className="text-[12.5px] font-medium">
        {label}
      </span>
      <Select
        items={items}
        value={value || null}
        onValueChange={(next) => onChange(String(next))}
        disabled={disabled || items.length === 0}
      >
        <SelectTrigger aria-labelledby={id} className="w-full">
          <SelectValue placeholder={empty} />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/** Segments is a row of choices in one group, one of them on. */
function Segments({
  label,
  value,
  options,
  onChange,
  disabled,
}: {
  label: string
  value: string
  options: { value: string; label: string }[]
  onChange: (value: string) => void
  disabled: boolean
}) {
  return (
    <RadioGroup
      value={value}
      onValueChange={onChange}
      disabled={disabled}
      aria-label={label}
      className="flex max-w-full gap-0.5 overflow-x-auto rounded-lg bg-muted p-0.5"
    >
      {options.map((option) => {
        const on = option.value === value
        return (
          <Radio.Root
            key={option.value}
            value={option.value}
            nativeButton
            render={<button type="button" />}
            className={cn(
              "h-7 shrink-0 rounded-md px-2.5 text-xs font-medium whitespace-nowrap disabled:opacity-60",
              on
                ? "bg-background text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {option.label}
          </Radio.Root>
        )
      })}
    </RadioGroup>
  )
}

/**
 * Connecting is the part of an engine's row for connecting to one that
 * already exists: a resource in the project, or a connection string that
 * is checked from the target before the plan can be reviewed. The
 * connection string goes to Vault on the API side when the plan is made.
 */
function Connecting({
  engine,
  state,
  resources,
  target,
  checking,
  disabled,
  onChange,
  onCheck,
}: {
  engine: Engine & { check: CheckEngine }
  state: Existing
  resources: string[]
  target?: string
  checking: boolean
  disabled: boolean
  onChange: (change: Partial<Existing>) => void
  onCheck: () => void
}) {
  const resource = resources.find((r) => r === state.resource)
  const result =
    target && state.check?.target === target ? state.check.result : undefined
  const message = result && checkMessage(engine.check, result)
  return (
    <div className="mt-2.5 grid gap-2">
      {resources.length > 0 && (
        <RadioGroup
          value={resource ?? ""}
          onValueChange={(value) => onChange({ resource: value || undefined })}
          disabled={disabled}
          aria-label={`${engine.name} source`}
          className="grid gap-1.5 sm:grid-cols-2"
        >
          {[...resources.map((r) => r as string | undefined), undefined].map(
            (r) => {
              const on = r === resource
              return (
                <Radio.Root
                  key={r ?? "string"}
                  value={r ?? ""}
                  nativeButton
                  render={<button type="button" />}
                  className={cn(
                    "truncate rounded-lg border px-2.5 py-1.5 text-left text-[12.5px] font-medium disabled:opacity-60",
                    on ? "border-brand-line bg-brand-soft" : "hover:bg-muted/50"
                  )}
                >
                  {r ? `${r} (in this project)` : "A connection string"}
                </Radio.Root>
              )
            }
          )}
        </RadioGroup>
      )}
      {!resource && (
        <>
          <div className="flex flex-wrap gap-2">
            <Input
              type="password"
              aria-label={engine.dsnLabel}
              placeholder={engine.dsnHint}
              value={state.dsn}
              disabled={disabled}
              // "off" doesn't stop a browser filling in a saved password.
              autoComplete="new-password"
              spellCheck={false}
              className="min-w-0 flex-[1_1_300px] font-mono text-[12.5px]"
              onChange={(event) =>
                // A different string is a different connection to check.
                onChange({
                  dsn: event.target.value,
                  check: undefined,
                  endpoint: undefined,
                })
              }
              onKeyDown={(event) => {
                if (event.key !== "Enter") return
                event.preventDefault()
                onCheck()
              }}
            />
            <Button
              type="button"
              variant="outline"
              disabled={disabled || checking || !state.dsn}
              onClick={onCheck}
            >
              {checking && <Spinner data-icon="inline-start" />}
              Check connection
            </Button>
          </div>
          {message && result && (
            <div
              role="note"
              className={cn(
                "grid gap-1 rounded-lg px-2.5 py-2 text-[12.5px]",
                message.tone === "ok"
                  ? "bg-overdye-tint text-overdye-ink"
                  : "bg-weld-tint text-weld-ink"
              )}
            >
              <span>{message.text}</span>
              {(result.notes ?? []).map((note) => (
                <span key={note}>{note}</span>
              ))}
            </div>
          )}
          <Hint>
            Not managed by Loomplane: no backups or restores. Fabriq only runs
            its own migrations on it.
          </Hint>
        </>
      )}
    </div>
  )
}

function Hint({ children }: { children: ReactNode }) {
  return <span className="text-xs text-muted-foreground">{children}</span>
}
