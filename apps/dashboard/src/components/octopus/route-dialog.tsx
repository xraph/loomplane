// Adding, editing and overriding one Octopus route: a domain and path, the
// methods it answers and the service it sends to. A new domain is attached
// through the Domains flow first, then the route lands on it.
import { useId, useState, type FormEvent } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useOctopusBoard,
  useOctopusCreateRoute,
  useOctopusRemoveRoute,
  useOctopusUpdateRoute,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type {
  Board,
  OctopusRouteInputBody,
  RouteResult,
  SyncResult,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  HostnameField,
  type HostnameValue,
} from "@/components/domains/hostname-field"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { reportSync } from "@/components/octopus/use-push-routes"
import { QueryError } from "@/components/query-error"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  exampleRequest,
  farpCollision,
  gatewayRouteInput,
  splitPath,
  validatePath,
  type RouteDialogMode,
  type RouteDraft,
} from "@/lib/octopus"
import { cn } from "@/lib/utils"

const methodChoices = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const

// The domain select's values: a hostname, any hostname, or a new one.
const anyHost = "any"
const attachHost = "attach"
const hostKey = (hostname: string) => (hostname ? `host:${hostname}` : anyHost)

type Field = "hostname" | "path" | "methods" | "service" | "note" | "form"
type Errors = Partial<Record<Field, string>>

/**
 * fieldOf is the field a server refusal is about, from the words
 * NormalizeRouteInput and the route checks use.
 */
function fieldOf(message: string): Field {
  if (/isn't a hostname|^Attach .+ first\.$/.test(message)) return "hostname"
  if (
    /^A path |^Drop the trailing slash|^A \* is only|^A route for |already answers|already has its own route/.test(
      message
    )
  )
    return "path"
  if (/HTTP method/.test(message)) return "methods"
  if (
    /^Pick the service|isn't a service|^The port must|runs on a different server|listens on \d+/.test(
      message
    )
  )
    return "service"
  if (/note/i.test(message)) return "note"
  return "form"
}

type ServiceOption = { value: string; service: string; port: number }

/**
 * serviceOptions are the services a route can send to: the ones the board
 * routes to and the ones nothing routes to yet, each once per port.
 */
function serviceOptions(board: Board, initial: RouteDraft): ServiceOption[] {
  const seen = new Map<string, ServiceOption>()
  const add = (service: string, port: number) => {
    if (!service || !port) return
    const value = `${service}:${port}`
    if (!seen.has(value)) seen.set(value, { value, service, port })
  }
  for (const host of board.hosts)
    for (const route of host.routes) add(route.service, route.port)
  for (const s of board.unrouted) add(s.service, s.port)
  add(initial.service ?? "", initial.port ?? 0)
  return [...seen.values()]
}

/**
 * hostOptions are the attached domains, then any hostname and a new one.
 * A route on a domain that's since been detached keeps its own.
 */
function hostOptions(
  board: Board,
  initial: RouteDraft
): { value: string; label: string }[] {
  const names = board.hosts
    .filter(
      (h) => h.hostname && (h.attached || h.hostname === initial.hostname)
    )
    .map((h) => h.hostname)
  if (initial.hostname && !names.includes(initial.hostname))
    names.push(initial.hostname)
  return [
    ...names.map((name) => ({ value: hostKey(name), label: name })),
    { value: anyHost, label: "Any hostname" },
    { value: attachHost, label: "Attach another domain…" },
  ]
}

export function RouteDialog(props: {
  projectId: string
  environmentId: string
  resource: string
  mode: RouteDialogMode
  initial?: RouteDraft
  open: boolean
  onOpenChange(open: boolean): void
}) {
  const { projectId, environmentId, resource, mode, open, onOpenChange } = props
  const board = useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  })
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const environment =
    environments.data?.environments.find((e) => e.id === environmentId)?.name ??
    environmentId

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="flex flex-col gap-0 overflow-hidden p-0 sm:max-w-[580px]"
      >
        <DialogHeader className="gap-1 px-5 pt-5 pb-1">
          <DialogTitle>
            {mode === "edit"
              ? `Edit a route on ${resource}`
              : `Add a route to ${resource}`}
          </DialogTitle>
          <DialogDescription>
            Send requests for a domain and path to a service in{" "}
            {project.data?.name ?? projectId} / {environment}. Loomplane pushes
            the route to Octopus without a redeploy.
          </DialogDescription>
        </DialogHeader>
        {board.data ? (
          <RouteForm {...props} board={board.data} />
        ) : board.status === "error" ? (
          <div className="px-5 pt-3 pb-4">
            <QueryError
              error={board.error}
              fallback="Octopus's routes couldn't be loaded."
              retry={board.refetch}
            />
          </div>
        ) : (
          <div aria-busy="true" className="grid gap-4 px-5 pt-3 pb-4">
            <Skeleton className="h-8 rounded-lg" />
            <Skeleton className="h-8 rounded-lg" />
            <Skeleton className="h-24 rounded-lg" />
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

const label = "flex flex-col gap-1.5 text-[12.5px] font-medium"
const hint = "text-xs font-normal text-muted-foreground"
const fieldError = "text-xs font-normal text-destructive"

function RouteForm({
  projectId,
  environmentId,
  resource,
  mode,
  initial = {},
  onOpenChange,
  board,
}: {
  projectId: string
  environmentId: string
  resource: string
  mode: RouteDialogMode
  initial?: RouteDraft
  onOpenChange(open: boolean): void
  board: Board
}) {
  const ids = useId()
  const invalidate = useInvalidate()
  const create = useMutation(useOctopusCreateRoute)
  const update = useMutation(useOctopusUpdateRoute)
  const remove = useMutation(useOctopusRemoveRoute)
  const lifecycle = useRouteLifecycle(projectId)

  const hosts = hostOptions(board, initial)
  const services = serviceOptions(board, initial)
  const [host, setHost] = useState(() =>
    initial.hostname !== undefined
      ? hostKey(initial.hostname)
      : (hosts[0]?.value ?? anyHost)
  )
  const [attach, setAttach] = useState<HostnameValue>({
    zoneId: "",
    hostname: "",
  })
  // The hostname attached on an earlier try, so a retry doesn't attach twice.
  const [attached, setAttached] = useState("")
  const [path, setPath] = useState(initial.path ?? "")
  const [methods, setMethods] = useState<string[]>(initial.methods ?? [])
  const [service, setService] = useState(() =>
    initial.service && initial.port
      ? `${initial.service}:${initial.port}`
      : (services[0]?.value ?? "")
  )
  const [strip, setStrip] = useState(initial.stripPrefix ?? false)
  const [note, setNote] = useState(initial.note ?? "")
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)

  const attaching = host === attachHost
  const hostname = attaching
    ? attach.hostname
    : host === anyHost
      ? ""
      : host.slice("host:".length)
  const target = services.find((s) => s.value === service)
  const { prefix } = splitPath(path || "/")
  const rootPath = prefix === "/"
  const example = exampleRequest({
    host: hostname,
    path,
    strip: strip && !rootPath,
    service: target?.service ?? "",
    port: target?.port ?? 0,
    privateUrl: board.gateway.privateUrl ?? "",
  })
  const collision = attaching ? undefined : farpCollision(board, hostname, path)
  const defaults = board.gateway.attach

  const fail = (err: unknown, fallback: string) => {
    const message = messageOf(err, fallback)
    if (statusOf(err) === 409) invalidate(useOctopusBoard)
    setErrors({ [fieldOf(message)]: message })
  }

  const done = (sync: SyncResult | undefined) => {
    invalidate(useOctopusBoard)
    reportSync(sync, "Saved. ")
    onOpenChange(false)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const pathError = validatePath(path)
    if (pathError) {
      setErrors({ path: pathError })
      return
    }
    if (!target) {
      setErrors({ service: "Pick the service this route sends traffic to." })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      // A new domain is attached first: the server refuses a route on a
      // hostname that isn't. One still waiting on DNS or its certificate
      // counts, so the route is created as soon as apply answers.
      if (attaching && attached !== hostname) {
        try {
          const op = await lifecycle.preview(
            gatewayRouteInput(defaults, { zoneId: attach.zoneId, hostname })
          )
          const applied = await lifecycle.apply(op)
          // An apply can answer with an operation that failed: the domain
          // isn't attached, so the route isn't created.
          if (applied.status === "failed") {
            setErrors({
              form: applied.message || "The domain wasn't attached.",
            })
            return
          }
          setAttached(hostname)
          // The new domain gets its own card even if the route is refused.
          invalidate(useOctopusBoard)
        } catch {
          // useRouteLifecycle words the refusal; it shows under Domain.
          return
        }
      }
      const body: OctopusRouteInputBody = {
        hostname,
        path,
        methods: methodChoices.filter((m) => methods.includes(m)),
        service: target.service,
        port: target.port,
        stripPrefix: strip && !rootPath,
        note: note.trim(),
      }
      const at = { projectId, environmentId, resource }
      const result =
        mode === "edit" && initial.routeId
          ? await update.mutateAsync({
              path: { ...at, routeId: initial.routeId },
              body: { ...body, revision: initial.revision },
            })
          : await create.mutateAsync({ path: at, body })
      // The generated mutation doesn't carry the response's type.
      done((result as RouteResult).sync)
    } catch (err) {
      fail(err, "The route wasn't saved.")
    } finally {
      setBusy(false)
    }
  }

  const deleteRoute = async () => {
    if (!initial.routeId) return
    setErrors({})
    setBusy(true)
    try {
      const sync = await remove.mutateAsync({
        path: { projectId, environmentId, resource, routeId: initial.routeId },
        body: { revision: initial.revision ?? 0 },
      })
      setConfirming(false)
      done(sync as SyncResult)
    } catch (err) {
      setConfirming(false)
      setErrors({ form: messageOf(err, "The route wasn't deleted.") })
    } finally {
      setBusy(false)
    }
  }

  const describe = (field: Field, ...more: string[]) =>
    [...more, errors[field] ? `${ids}-${field}-error` : ""]
      .filter(Boolean)
      .join(" ") || undefined
  const errorOf = (field: Field) =>
    errors[field] && (
      <span id={`${ids}-${field}-error`} className={fieldError}>
        {errors[field]}
      </span>
    )
  const domainError = errors.hostname || (attaching ? lifecycle.error : "")

  return (
    <>
      <form
        onSubmit={(e) => void submit(e)}
        noValidate
        className="flex min-h-0 flex-1 flex-col"
      >
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-5 pt-3 pb-4">
          <div
            role="group"
            aria-labelledby={`${ids}-host-label`}
            className="flex flex-col gap-1.5"
          >
            <div className={label}>
              <span id={`${ids}-host-label`}>Domain</span>
              <Select
                items={hosts}
                value={host}
                onValueChange={(next) => {
                  setHost(String(next))
                  setErrors({})
                }}
                disabled={busy}
              >
                <SelectTrigger
                  aria-labelledby={`${ids}-host-label`}
                  aria-invalid={!!domainError || undefined}
                  className="w-full font-mono"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {hosts.map((o) => (
                    <SelectItem
                      key={o.value}
                      value={o.value}
                      className={cn(o.value.startsWith("host:") && "font-mono")}
                    >
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {attaching && (
              <div className="flex flex-col gap-1.5">
                <HostnameField
                  projectId={projectId}
                  value={attach}
                  onChange={setAttach}
                />
                <span className="text-xs text-muted-foreground">
                  Loomplane adds the DNS record if the zone is connected, then
                  issues a certificate. The route starts serving once both check
                  out.
                </span>
              </div>
            )}
            {domainError && (
              <span role="alert" className={fieldError}>
                {domainError}
              </span>
            )}
          </div>

          <div className={label}>
            <label htmlFor={`${ids}-path`}>Path</label>
            <Input
              id={`${ids}-path`}
              type="text"
              value={path}
              onChange={(e) => {
                setPath(e.target.value)
                setErrors({})
              }}
              placeholder="/auth/*"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={!!errors.path || undefined}
              aria-describedby={describe("path", `${ids}-path-hint`)}
              className="font-mono"
            />
            <span id={`${ids}-path-hint`} className={hint}>
              End with <span className="font-mono">/*</span> to match everything
              under it.
            </span>
            {errorOf("path")}
          </div>

          <div className="flex flex-col gap-1.5">
            <span
              id={`${ids}-methods-label`}
              className="text-[12.5px] font-medium"
            >
              Methods{" "}
              {methods.length === 0 && (
                <span className="font-normal text-muted-foreground">
                  · any method until you pick some
                </span>
              )}
            </span>
            <ToggleGroup
              multiple
              value={methods}
              onValueChange={(next) => setMethods(next as string[])}
              aria-labelledby={`${ids}-methods-label`}
              spacing={1.5}
              className="flex-wrap"
              disabled={busy}
            >
              {methodChoices.map((m) => (
                <ToggleGroupItem
                  key={m}
                  value={m}
                  variant="outline"
                  size="sm"
                  className="font-mono text-xs aria-pressed:border-brand-line aria-pressed:bg-brand-soft"
                >
                  {m}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
            {errorOf("methods")}
          </div>

          <div className={label}>
            <span id={`${ids}-service-label`}>Send to</span>
            <Select
              items={services.map((s) => ({
                value: s.value,
                label: `${s.service} · port ${s.port}`,
              }))}
              value={service || null}
              onValueChange={(next) => {
                setService(String(next))
                setErrors({})
              }}
              disabled={busy || services.length === 0}
            >
              <SelectTrigger
                aria-labelledby={`${ids}-service-label`}
                aria-invalid={!!errors.service || undefined}
                aria-describedby={describe("service")}
                className="w-full"
              >
                <SelectValue placeholder="No service has an HTTP port" />
              </SelectTrigger>
              <SelectContent>
                {services.map((s) => (
                  <SelectItem key={s.value} value={s.value}>
                    {s.service} · port {s.port}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {errorOf("service")}
          </div>

          <div className={label}>
            <span>
              <label htmlFor={`${ids}-note`}>Note</label>{" "}
              <span className="font-normal text-muted-foreground">
                optional
              </span>
            </span>
            <Input
              id={`${ids}-note`}
              type="text"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              maxLength={120}
              aria-invalid={!!errors.note || undefined}
              aria-describedby={describe("note")}
            />
            {errorOf("note")}
          </div>

          <label
            className={cn(
              "flex items-center gap-2 text-[13px]",
              rootPath && "text-muted-foreground"
            )}
          >
            <Checkbox
              checked={strip && !rootPath}
              onCheckedChange={(next) => setStrip(next === true)}
              disabled={rootPath || busy}
            />
            <span>
              Strip <span className="font-mono text-[12.5px]">{prefix}</span>{" "}
              before forwarding
            </span>
          </label>

          <div
            role="group"
            aria-labelledby={`${ids}-example-label`}
            className="grid gap-1 rounded-lg bg-muted px-3 py-2.5"
          >
            <span
              id={`${ids}-example-label`}
              className="text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
            >
              Example request
            </span>
            <span className="font-mono text-[12.5px] leading-relaxed break-all">
              {example.in}
            </span>
            <span className="font-mono text-[12.5px] leading-relaxed break-all text-muted-foreground">
              → {example.out}
            </span>
          </div>

          {collision && (
            <div
              role="note"
              className="rounded-lg bg-weld-tint px-3 py-2.5 text-[12.5px] text-weld-ink"
            >
              FARP also publishes <span className="font-mono">{path}</span> on
              this domain from{" "}
              <b className="font-semibold">{collision.service}</b>. Your route
              takes priority while it exists, and the FARP route comes back if
              you delete it.
            </div>
          )}
        </div>

        {errors.form && (
          <p role="alert" className="px-5 pb-3 text-[12.5px] text-destructive">
            {errors.form}
          </p>
        )}

        <DialogFooter className="m-0 px-5 py-3">
          {mode === "edit" && initial.routeId && (
            <Button
              type="button"
              variant="destructive"
              className="sm:mr-auto"
              disabled={busy}
              onClick={() => setConfirming(true)}
            >
              Delete route
            </Button>
          )}
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={
              busy || lifecycle.pending || (attaching && !attach.zoneId)
            }
          >
            {mode === "edit" ? "Save route" : "Add route"}
          </Button>
        </DialogFooter>
      </form>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete route</AlertDialogTitle>
            <AlertDialogDescription>
              Delete {initial.path} on {initial.hostname || "any host"}? If FARP
              publishes the same path, its route comes back.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              onClick={() => void deleteRoute()}
            >
              Delete route
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
