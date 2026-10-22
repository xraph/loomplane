// Where a catalog kind gets each of its dependencies: a new one Loomplane
// runs beside it, a matching resource already in the project's manifest,
// or an external endpoint Loomplane connects to and leaves alone.
import { PlugIcon, PlusIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useCatalogEndpointsCreate,
  useCatalogEndpointsList,
} from "@/client/forge/loomplane/hooks"
import type {
  CatalogconnectEndpoint,
  DependencyInfo,
  KindInfo,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
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
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import type { DependencyPicks } from "@/lib/catalog-add"
import { catalogKind } from "@/lib/catalog-kinds"
import { useOrganization } from "@/lib/organization"
import { protectionLabel, type DependencyChoice } from "@/lib/service-recovery"
import { cn } from "@/lib/utils"

/** A resource in the project's manifest that a dependency could connect. */
export type ManifestResource = { name: string; kind: string; title?: string }

// The engines Loomplane connects external endpoints for.
const externalEngines = ["postgres", "redis"]

/**
 * CatalogDependencyPicker chooses, for each dependency of kind, where it
 * comes from. resources are the project's manifest resources and
 * endpoints the organization's external endpoints; only those of a kind
 * the dependency takes are offered. An optional dependency can be turned
 * off.
 */
export function CatalogDependencyPicker({
  kind,
  resources,
  endpoints,
  value,
  onChange,
  disabled = false,
}: {
  kind: KindInfo
  resources: ManifestResource[]
  endpoints: CatalogconnectEndpoint[]
  value: DependencyPicks
  onChange: (value: DependencyPicks) => void
  disabled?: boolean
}) {
  // Endpoints added here show at once, before the list is read again.
  const [added, setAdded] = useState<CatalogconnectEndpoint[]>([])
  const known = new Set(endpoints.map((e) => e.id))
  const all = [...endpoints, ...added.filter((e) => !known.has(e.id))]
  return (
    <div className="grid gap-3">
      {(kind.dependencies ?? []).map((dep) => (
        <Dependency
          key={dep.name}
          dep={dep}
          resources={resources.filter((r) => dep.kinds.includes(r.kind))}
          endpoints={
            dep.external ? all.filter((e) => dep.kinds.includes(e.engine)) : []
          }
          pick={value[dep.name] ?? { mode: "off" }}
          onPick={(pick) => onChange({ ...value, [dep.name]: pick })}
          onAdded={(endpoint) => {
            setAdded((list) => [...list, endpoint])
            onChange({
              ...value,
              [dep.name]: {
                mode: "external",
                external: endpoint.id,
                kind: endpoint.engine,
              },
            })
          }}
          disabled={disabled}
        />
      ))}
    </div>
  )
}

function kindTitle(kind: string, fallback?: string): string {
  return catalogKind(kind)?.title ?? fallback ?? kind
}

type Option = {
  key: string
  label: string
  detail: string
  pick: DependencyChoice
  on: boolean
}

function Dependency({
  dep,
  resources,
  endpoints,
  pick,
  onPick,
  onAdded,
  disabled,
}: {
  dep: DependencyInfo
  resources: ManifestResource[]
  endpoints: CatalogconnectEndpoint[]
  pick: DependencyChoice
  onPick: (pick: DependencyChoice) => void
  onAdded: (endpoint: CatalogconnectEndpoint) => void
  disabled: boolean
}) {
  const { can } = useOrganization()
  const [adding, setAdding] = useState(false)
  const id = useId()
  const on = pick.mode !== "off"
  const fresh: DependencyChoice = { mode: "new", kind: dep.kinds[0] }
  const options: Option[] = [
    {
      key: "new",
      label: "Provision new",
      detail: `Loomplane runs a new ${kindTitle(dep.kinds[0] ?? dep.name, dep.title)} for it`,
      pick: fresh,
      on: pick.mode === "new",
    },
    ...resources.map((r) => ({
      key: `resource:${r.name}`,
      label: `${r.name} (${kindTitle(r.kind, r.title)})`,
      detail: "Already in this project's manifest",
      pick: { mode: "resource" as const, resource: r.name, kind: r.kind },
      on: pick.mode === "resource" && pick.resource === r.name,
    })),
    ...endpoints.map((e) => ({
      key: `external:${e.id}`,
      label: `${e.name} (external)`,
      detail: `${e.host}:${e.port}`,
      pick: { mode: "external" as const, external: e.id, kind: e.engine },
      on: pick.mode === "external" && pick.external === e.id,
    })),
  ]
  const engines = dep.kinds.filter((k) => externalEngines.includes(k))
  const canAdd = dep.external && engines.length > 0 && can("secret:manage")
  return (
    <div
      role="group"
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-kinds`}
      className="grid gap-3 rounded-xl bg-card p-3.5 ring-1 ring-border"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="grid min-w-0 gap-0.5">
          <span className="flex flex-wrap items-baseline gap-x-2 text-[13px] font-semibold">
            <span id={`${id}-title`}>{dep.title}</span>
            <span className="font-mono text-[11.5px] font-normal text-muted-foreground">
              {dep.name}
            </span>
            {dep.optional && (
              <span className="rounded-md bg-muted px-1.5 py-px text-[11px] font-normal text-muted-foreground">
                Optional
              </span>
            )}
          </span>
          <span
            id={`${id}-kinds`}
            className="truncate text-xs text-muted-foreground"
          >
            Takes {dep.kinds.map((k) => kindTitle(k)).join(" or ")}
          </span>
        </span>
        {dep.optional && (
          <Switch
            aria-label={`Use ${dep.title}`}
            checked={on}
            disabled={disabled}
            onCheckedChange={(checked) =>
              onPick(checked ? fresh : { mode: "off" })
            }
          />
        )}
      </div>
      {on && (
        <>
          <div
            role="radiogroup"
            aria-label={dep.title}
            className="grid gap-2 sm:grid-cols-2"
          >
            {options.map((o) => (
              <button
                key={o.key}
                type="button"
                role="radio"
                aria-checked={o.on}
                aria-label={o.label}
                aria-describedby={`${id}-${o.key}`}
                disabled={disabled}
                onClick={() => onPick(o.pick)}
                className={cn(
                  "flex min-w-0 items-center gap-2.5 rounded-lg bg-card px-3 py-2 text-left ring-1 transition-shadow disabled:opacity-60",
                  o.on
                    ? "ring-2 ring-primary"
                    : "ring-border hover:ring-foreground/25"
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "flex size-3.5 shrink-0 items-center justify-center rounded-full ring-1",
                    o.on ? "ring-primary" : "ring-border"
                  )}
                >
                  {o.on && (
                    <span className="size-1.5 rounded-full bg-primary" />
                  )}
                </span>
                <span className="grid min-w-0 gap-0.5">
                  <span className="truncate text-[13px] font-medium">
                    {o.label}
                  </span>
                  <span
                    id={`${id}-${o.key}`}
                    className="truncate text-xs text-muted-foreground"
                  >
                    {o.detail}
                  </span>
                </span>
              </button>
            ))}
          </div>
          {pick.mode === "external" && (
            <p className="flex items-start gap-2 rounded-lg bg-muted px-3 py-2 text-[12.5px] text-muted-foreground">
              <PlugIcon className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              {protectionLabel("external")}
            </p>
          )}
          {canAdd && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="justify-self-start"
              disabled={disabled}
              onClick={() => setAdding(true)}
            >
              <PlusIcon data-icon="inline-start" />
              Add an external endpoint
            </Button>
          )}
        </>
      )}
      {canAdd && (
        <Dialog open={adding} onOpenChange={setAdding}>
          <DialogContent className="sm:max-w-md">
            {adding && (
              <EndpointForm
                dep={dep}
                engines={engines}
                close={() => setAdding(false)}
                onAdded={onAdded}
              />
            )}
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

/**
 * EndpointForm registers an external endpoint. The connection string goes
 * to Vault on the API side and is cleared from the form once it's sent.
 */
function EndpointForm({
  dep,
  engines,
  close,
  onAdded,
}: {
  dep: DependencyInfo
  engines: string[]
  close: () => void
  onAdded: (endpoint: CatalogconnectEndpoint) => void
}) {
  const create = useMutation(useCatalogEndpointsCreate)
  const invalidate = useInvalidate()
  const id = useId()
  const [name, setName] = useState("")
  const [engine, setEngine] = useState(engines[0])
  const [dsn, setDsn] = useState("")
  const [error, setError] = useState("")
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    // The dialog is portalled out of the page's form, but React still
    // bubbles this submit up to it.
    event.stopPropagation()
    setError("")
    try {
      const endpoint = await create.mutateAsync({
        body: { name: name.trim(), engine, dsn },
      })
      setDsn("")
      invalidate(useCatalogEndpointsList)
      onAdded(endpoint)
      close()
    } catch (cause) {
      setError(messageOf(cause, "The endpoint wasn't saved."))
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      <DialogHeader>
        <DialogTitle>Add an external endpoint</DialogTitle>
        <DialogDescription>
          For {dep.title}. Loomplane keeps the connection string in Vault and
          never shows it again. It doesn&apos;t back up, restore or change
          anything there beyond the schema the service migrates.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="py-4">
        <Field>
          <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
          <Input
            id={`${id}-name`}
            value={name}
            maxLength={100}
            required
            autoFocus
            placeholder="prod-pg"
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        {engines.length > 1 && (
          <Field>
            <FieldLabel>Engine</FieldLabel>
            <div
              role="radiogroup"
              aria-label="Engine"
              className="flex gap-0.5 self-start rounded-[10px] bg-muted p-[3px]"
            >
              {engines.map((e) => (
                <button
                  key={e}
                  type="button"
                  role="radio"
                  aria-checked={e === engine}
                  onClick={() => setEngine(e)}
                  className={cn(
                    "h-7 rounded-lg px-2.5 text-[12.5px] font-medium",
                    e === engine
                      ? "bg-background text-foreground shadow-xs"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  {kindTitle(e)}
                </button>
              ))}
            </div>
          </Field>
        )}
        <Field>
          <FieldLabel htmlFor={`${id}-dsn`}>Connection string</FieldLabel>
          <Input
            id={`${id}-dsn`}
            type="password"
            className="font-mono"
            value={dsn}
            required
            // "off" doesn't stop a browser filling in a saved password.
            autoComplete="new-password"
            spellCheck={false}
            placeholder={
              engine === "redis"
                ? "rediss://user:password@host:6379/0"
                : "postgres://user:password@host:5432/app"
            }
            onChange={(e) => setDsn(e.target.value)}
          />
          <FieldDescription>
            Loomplane checks it can reach the endpoint when it plans and on each
            release.
          </FieldDescription>
        </Field>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </FieldGroup>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={close}>
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={create.isPending || !name.trim() || !dsn}
        >
          {create.isPending && <Spinner data-icon="inline-start" />}
          Save endpoint
        </Button>
      </DialogFooter>
    </form>
  )
}
