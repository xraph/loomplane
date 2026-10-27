// The Domains tab: the hostnames attached to a gateway, each a Loomplane
// route to Octopus on port 8080, with where FARP routes land and the way
// to attach or detach one.
import { PlusIcon } from "lucide-react"
import { useId, useState, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDomainRoutesList,
  useDomainsProject,
  useOctopusBoard,
  useOctopusSetFarpHostnames,
  useOctopusStatus,
} from "@/client/forge/loomplane/hooks"
import type {
  Board,
  CloudinfraRoute,
  HostCard,
  RouteOperation,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { RouteStates } from "@/components/domains/domain-states"
import {
  HostnameField,
  type HostnameValue,
} from "@/components/domains/hostname-field"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
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
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Switch } from "@/components/ui/switch"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import {
  attachedHosts,
  farpHostnamesWith,
  farpLanding,
  farpLandingCount,
  gatewayPort,
  gatewayRouteInput,
  manualRoutes,
  routeCountLabel,
} from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

/** The columns of a hostname row: name, the three states, FARP, actions. */
const columns =
  "grid grid-cols-[minmax(0,1.3fr)_110px_140px_160px_190px_minmax(176px,auto)] gap-3"

export function DomainsPanel(props: CatalogPanelProps) {
  const { projectId, environmentId, resource } = props
  const board = useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  })

  if (board.status === "error" && !board.data)
    return (
      <ReadFailed
        error={board.error}
        retry={board.refetch}
        denied="You can't see this gateway's domains"
        fallback="Octopus's domains couldn't be loaded."
      />
    )
  if (!board.data) return <PanelLoading label="Loading domains" />
  return (
    <div className="grid min-w-0 gap-3.5">
      {/* A refresh that fails keeps what was read before on screen. */}
      {board.status === "error" && (
        <QueryError
          error={board.error}
          fallback="Octopus's domains couldn't be loaded."
          retry={board.refetch}
        />
      )}
      <Domains {...props} board={board.data} />
    </div>
  )
}

function Domains({
  projectId,
  environmentId,
  resource,
  board,
}: CatalogPanelProps & { board: Board }) {
  const { can } = useOrganization()
  const writes = can("infrastructure:modify")
  const invalidate = useInvalidate()
  // The domain routes carry each hostname's zone and the revision a
  // detach needs; the board only names the route.
  const routes = useQuery(useDomainRoutesList, { path: { projectId } })
  const zones = useQuery(useDomainsProject, { path: { projectId } })
  const setFarp = useMutation(useOctopusSetFarpHostnames)
  // Each opening of the attach dialog starts fresh.
  const [attach, setAttach] = useState(0)
  const [detaching, setDetaching] = useState<HostCard | null>(null)

  const hosts = attachedHosts(board)
  const noZones =
    !!zones.data && !zones.data.domains.some((domain) => domain.verified)
  const domainRoute = (host: HostCard): CloudinfraRoute | undefined =>
    routes.data?.routes.find((r) => r?.id === host.domainRouteId) ?? undefined
  const openAttach = () => setAttach((n) => n + 1)

  const toggleFarp = async (host: HostCard, on: boolean) => {
    try {
      await setFarp.mutateAsync({
        path: { projectId, environmentId, resource },
        body: {
          hostnames: farpHostnamesWith(board, host.hostname, on),
          revision: board.gateway.revision,
        },
      })
    } catch (err) {
      toast.error(messageOf(err, "Where FARP routes land wasn't changed."))
      if (statusOf(err) !== 409) return
    }
    invalidate(useOctopusBoard)
    invalidate(useOctopusStatus)
  }

  return (
    <>
      <section
        aria-label="Domains"
        className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[14px] border border-border bg-subtle px-4 py-3"
      >
        <span className="flex-[1_1_360px] text-[13px] leading-normal">
          Domains attached to <b className="font-semibold">{resource}</b>. Each
          one is a Loomplane route to Octopus on port {gatewayPort}, and Octopus
          routes by host and path behind it.
        </span>
        {writes && hosts.length > 0 && (
          <Button type="button" onClick={openAttach}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            Attach a domain
          </Button>
        )}
      </section>

      {hosts.length > 0 ? (
        <div className="overflow-hidden rounded-[14px] border border-border">
          <div className="overflow-x-auto">
            <div
              role="table"
              aria-label={`Domains on ${resource}`}
              className="min-w-[960px]"
            >
              <div
                role="row"
                className={cn(
                  columns,
                  "border-b border-border bg-subtle px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
                )}
              >
                {["Hostname", "DNS", "Certificate", "Route", "FARP"].map(
                  (h) => (
                    <span key={h} role="columnheader">
                      {h}
                    </span>
                  )
                )}
                <span role="columnheader" className="sr-only">
                  Actions
                </span>
              </div>
              {hosts.map((host) => (
                <HostnameRow
                  key={host.hostname}
                  host={host}
                  zoneId={domainRoute(host)?.input.zoneId}
                  farpOn={farpLanding(board, host.hostname)}
                  farpLast={
                    farpLanding(board, host.hostname) &&
                    farpLandingCount(board) === 1
                  }
                  busy={setFarp.isPending}
                  onFarp={
                    writes ? (on) => void toggleFarp(host, on) : undefined
                  }
                  onDetach={writes ? () => setDetaching(host) : undefined}
                />
              ))}
            </div>
          </div>
        </div>
      ) : (
        <ZeroState>
          <ZeroStateHeader>
            <ZeroStateTitle>No domains yet</ZeroStateTitle>
            {noZones ? (
              <ZeroStateDescription>
                Your organization has no zones this project can use. Add one on
                the <Link to="/domains">Domains page</Link>.
              </ZeroStateDescription>
            ) : (
              <ZeroStateDescription>
                Octopus answers on the target&apos;s private network until you
                attach one. Pick a hostname from your organization&apos;s zones.
              </ZeroStateDescription>
            )}
          </ZeroStateHeader>
          {writes && !noZones && (
            <ZeroStateContent>
              <Button type="button" onClick={openAttach}>
                <PlusIcon data-icon="inline-start" aria-hidden />
                Attach a domain
              </Button>
            </ZeroStateContent>
          )}
        </ZeroState>
      )}

      {writes && attach > 0 && (
        <AttachDialog
          key={attach}
          projectId={projectId}
          board={board}
          onClose={() => setAttach(0)}
        />
      )}
      {writes && detaching && (
        <DetachDialog
          projectId={projectId}
          host={detaching}
          route={domainRoute(detaching)}
          routesRead={!!routes.data || routes.status === "error"}
          onClose={() => setDetaching(null)}
        />
      )}
    </>
  )
}

function HostnameRow({
  host,
  zoneId,
  farpOn,
  farpLast,
  busy,
  onFarp,
  onDetach,
}: {
  host: HostCard
  /** The hostname's zone, once the domain routes are read. */
  zoneId?: string
  farpOn: boolean
  /** This is the only hostname FARP routes land on, so it stays on. */
  farpLast: boolean
  busy: boolean
  /** Turns FARP on or off here, or undefined when the viewer can't. */
  onFarp?: (on: boolean) => void
  onDetach?: () => void
}) {
  const ids = useId()
  const manual = manualRoutes(host)
  return (
    <div
      role="row"
      className={cn(
        columns,
        "items-center border-border px-4 py-2.5 transition-colors not-first:border-t hover:bg-subtle"
      )}
    >
      <span role="cell" className="flex min-w-0 flex-col gap-0.5">
        <span
          data-hostname
          className="font-mono text-[13px] font-medium break-all"
        >
          {host.hostname}
        </span>
        <span className="text-xs text-muted-foreground">
          {routeCountLabel(host.routes.length)}
        </span>
      </span>
      {host.status ? (
        <RouteStates status={host.status} />
      ) : (
        <>
          <span role="cell" />
          <span role="cell" />
          <span role="cell" />
        </>
      )}
      <span role="cell" className="flex items-center gap-2">
        {onFarp && farpLast ? (
          <Blocked reason="FARP routes have to land somewhere. Turn on another hostname first.">
            <Switch aria-labelledby={`${ids}-farp`} checked disabled />
          </Blocked>
        ) : (
          <Switch
            aria-labelledby={`${ids}-farp`}
            checked={farpOn}
            disabled={!onFarp || busy}
            onCheckedChange={(on) => onFarp?.(on)}
          />
        )}
        <span id={`${ids}-farp`} className="text-[12.5px] text-foreground/80">
          FARP routes land here
        </span>
      </span>
      <span role="cell" className="flex items-center justify-end gap-1.5">
        <Link
          to="/domains"
          search={zoneId ? { zone: zoneId } : {}}
          className={cn(
            buttonVariants({ variant: "ghost", size: "sm" }),
            "text-xs"
          )}
        >
          Zone settings
        </Link>
        {onDetach &&
          (manual > 0 ? (
            <Blocked
              reason={`Move or delete its ${routeCountLabel(manual)} first.`}
            >
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="text-xs"
                disabled
              >
                Detach
              </Button>
            </Blocked>
          ) : (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="text-xs"
              onClick={onDetach}
            >
              Detach
            </Button>
          ))}
      </span>
    </div>
  )
}

/**
 * Blocked is a disabled control with the reason it can't be used. A
 * disabled control gets no pointer events, so the reason hangs on what
 * wraps it.
 */
function Blocked({
  reason,
  children,
}: {
  reason: string
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        delay={0}
        render={<span tabIndex={0} className="inline-flex" />}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  )
}

function DetachDialog({
  projectId,
  host,
  route,
  routesRead,
  onClose,
}: {
  projectId: string
  host: HostCard
  /** The domain route to remove, once the domain routes are read. */
  route?: CloudinfraRoute
  /** The domain routes answered, or failed to: a missing route won't come. */
  routesRead: boolean
  onClose(): void
}) {
  const invalidate = useInvalidate()
  const lifecycle = useRouteLifecycle(projectId)

  const detach = async () => {
    if (!route) return
    try {
      await lifecycle.remove(route)
      invalidate(useOctopusBoard)
      onClose()
    } catch {
      // useRouteLifecycle words the refusal.
    }
  }

  // The dialog stays open on a refusal, so the reason shows here.
  const shown =
    lifecycle.error ||
    (!route && routesRead
      ? "Loomplane couldn't read this domain's route. Try again in a moment."
      : "")
  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Detach domain</AlertDialogTitle>
          <AlertDialogDescription>
            Detach <span className="font-mono">{host.hostname}</span>? Loomplane
            removes its DNS record and stops serving it.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {shown && (
          <p role="alert" className="text-[12.5px] text-destructive">
            {shown}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={lifecycle.pending}>
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={lifecycle.pending || !route}
            onClick={() => void detach()}
          >
            Detach
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * AttachDialog previews the gateway route for a hostname, shows the DNS
 * record it adds, then applies it. A hostname changed after the preview
 * is previewed again.
 */
function AttachDialog({
  projectId,
  board,
  onClose,
}: {
  projectId: string
  board: Board
  onClose(): void
}) {
  const ids = useId()
  const invalidate = useInvalidate()
  const lifecycle = useRouteLifecycle(projectId)
  const [value, setValue] = useState<HostnameValue>({
    zoneId: "",
    hostname: "",
  })
  const [preview, setPreview] = useState<{
    op: RouteOperation
    for: HostnameValue
  } | null>(null)
  const [error, setError] = useState("")

  const current =
    preview &&
    preview.for.zoneId === value.zoneId &&
    preview.for.hostname === value.hostname
      ? preview.op
      : undefined
  const record = current?.desiredRecord

  const run = async () => {
    setError("")
    try {
      if (!current) {
        const op = await lifecycle.preview(
          gatewayRouteInput(board.gateway.attach, value)
        )
        setPreview({ op, for: value })
        return
      }
      const op = await lifecycle.apply(current)
      if (op.status === "failed") {
        setPreview(null)
        setError(op.message || "The route wasn't applied.")
        return
      }
      invalidate(useOctopusBoard)
      onClose()
    } catch {
      // useRouteLifecycle words the refusal. A refused apply starts over
      // from a fresh preview.
      if (current) setPreview(null)
    }
  }

  const shown = error || lifecycle.error
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="gap-0 overflow-hidden rounded-2xl p-0 sm:max-w-[580px]"
      >
        <DialogHeader className="gap-1 px-5 pt-[18px] pb-1.5">
          <DialogTitle className="text-[17px] leading-normal font-semibold tracking-[-0.015em]">
            Attach a domain
          </DialogTitle>
        </DialogHeader>
        <form
          noValidate
          className="contents"
          onSubmit={(e) => {
            e.preventDefault()
            void run()
          }}
        >
          <div className="flex flex-col gap-3.5 px-5 pt-3 pb-4">
            <div className="flex flex-col gap-1.5">
              <HostnameField
                projectId={projectId}
                value={value}
                onChange={setValue}
              />
              <span className="text-xs leading-normal text-muted-foreground">
                Loomplane adds the DNS record if the zone is connected, then
                issues a certificate. The route starts serving once both check
                out.
              </span>
            </div>
            {record?.name && (
              <div
                role="group"
                aria-labelledby={`${ids}-record`}
                className="flex flex-col gap-1 rounded-[10px] bg-muted px-3 py-2.5"
              >
                <span
                  id={`${ids}-record`}
                  className="text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
                >
                  DNS record
                </span>
                <span className="flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-[12.5px] leading-[1.6] break-all text-foreground">
                  <span>{record.name}</span>
                  <span className="text-muted-foreground">{record.type}</span>
                  <span>{record.value}</span>
                </span>
              </div>
            )}
            {shown && (
              <span role="alert" className="text-xs text-destructive">
                {shown}
              </span>
            )}
          </div>
          <DialogFooter className="mx-0 mb-0 gap-2 rounded-none border-border bg-subtle px-5 py-3">
            <Button
              type="button"
              variant="outline"
              disabled={lifecycle.pending}
              onClick={onClose}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={lifecycle.pending || !value.zoneId || !value.hostname}
            >
              {current ? "Attach" : "Preview"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
