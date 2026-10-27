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
import {
  HostnameField,
  type HostnameValue,
} from "@/components/domains/hostname-field"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { StrandedFarp } from "@/components/octopus/discovery-strip"
import { pillTones } from "@/components/octopus/host-card"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
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
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
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
  certificateLabel,
  dnsLabel,
  dnsTone,
  routeLabel,
  stateTone,
  type DomainState,
} from "@/lib/domains"
import {
  attachedHosts,
  farpHostnamesWith,
  farpLanding,
  farpLandingCount,
  farpLandings,
  farpStranded,
  gatewayPort,
  gatewayRouteInput,
  manualRoutes,
  routeCountLabel,
} from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

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
  const stranded = farpStranded(board)
  const landings = farpLandings(board)
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
      <Panel
        aria-label="Domains"
        className="flex-row flex-wrap items-center gap-x-4 gap-y-2.5 bg-subtle py-3"
      >
        <span className="flex-[1_1_360px] text-[13px]">
          Domains attached to <b className="font-semibold">{resource}</b>. Each
          one is a Loomplane route to Octopus on port {gatewayPort}, and Octopus
          routes by host and path behind it.
          {stranded.length > 0 && (
            <span className="mt-1 block text-weld-ink">
              <StrandedFarp hosts={stranded} />
            </span>
          )}
        </span>
        {writes && hosts.length > 0 && (
          <Button type="button" onClick={openAttach}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            Attach a domain
          </Button>
        )}
      </Panel>

      {hosts.length > 0 ? (
        <Card className="py-0">
          <Table
            aria-label={`Domains on ${resource}`}
            className="min-w-[960px]"
          >
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Hostname</TableHead>
                <TableHead>DNS</TableHead>
                <TableHead>Certificate</TableHead>
                <TableHead>Route</TableHead>
                <TableHead>FARP</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
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
            </TableBody>
          </Table>
        </Card>
      ) : (
        <ZeroState>
          <ZeroStateHeader>
            <ZeroStateTitle>No domains yet</ZeroStateTitle>
            {noZones ? (
              <ZeroStateDescription>
                Your workspace has no zones this project can use. Add one on the{" "}
                <Link to="/domains">Domains page</Link>.
              </ZeroStateDescription>
            ) : (
              <ZeroStateDescription>
                Octopus answers on the target&apos;s private network until you
                attach one. Pick a hostname from your workspace&apos;s zones.
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
          lastFarp={
            hosts.length > 1 &&
            landings.length === 1 &&
            landings[0] === detaching.hostname
          }
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
  const states = host.status && [
    [dnsLabel(host.status), dnsTone(host.status)] as const,
    [
      certificateLabel(host.status),
      stateTone(host.status.certificate as DomainState),
    ] as const,
    [
      routeLabel(host.status),
      stateTone(host.status.route as DomainState),
    ] as const,
  ]
  return (
    <TableRow>
      <TableCell className="whitespace-normal">
        <span className="flex min-w-0 flex-col gap-0.5">
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
      </TableCell>
      {states ? (
        states.map(([label, tone], i) => (
          <TableCell key={i}>
            <StatusPill tone={pillTones[tone]}>{label}</StatusPill>
          </TableCell>
        ))
      ) : (
        <>
          <TableCell />
          <TableCell />
          <TableCell />
        </>
      )}
      <TableCell>
        <span className="flex items-center gap-2">
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
          <span id={`${ids}-farp`} className="text-[12.5px]">
            FARP routes land here
          </span>
        </span>
      </TableCell>
      <TableCell>
        <span className="flex items-center justify-end gap-1.5">
          <Link
            to="/domains"
            search={zoneId ? { zone: zoneId } : {}}
            className={buttonVariants({ variant: "ghost", size: "sm" })}
          >
            Zone settings
          </Link>
          {onDetach &&
            (manual > 0 ? (
              <Blocked
                reason={`Move or delete its ${routeCountLabel(manual)} first.`}
              >
                <Button type="button" variant="outline" size="sm" disabled>
                  Detach
                </Button>
              </Blocked>
            ) : (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onDetach}
              >
                Detach
              </Button>
            ))}
        </span>
      </TableCell>
    </TableRow>
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
  lastFarp,
  route,
  routesRead,
  onClose,
}: {
  projectId: string
  host: HostCard
  /** FARP routes land only on this hostname, and others stay attached. */
  lastFarp: boolean
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
            {lastFarp && (
              <span className="mt-2 block">
                FARP routes land only here. After you detach it they answer only
                on the target&apos;s private network until you pick another
                hostname.
              </span>
            )}
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
        className="flex flex-col gap-0 overflow-hidden p-0 sm:max-w-[580px]"
      >
        <DialogHeader className="gap-1 px-5 pt-5 pb-1">
          <DialogTitle>Attach a domain</DialogTitle>
        </DialogHeader>
        <form
          noValidate
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(e) => {
            e.preventDefault()
            void run()
          }}
        >
          <div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-5 pt-3 pb-4">
            <div className="flex flex-col gap-1.5">
              <HostnameField
                projectId={projectId}
                value={value}
                onChange={setValue}
              />
              <span className="text-xs text-muted-foreground">
                Loomplane adds the DNS record if the zone is connected, then
                issues a certificate. The route starts serving once both check
                out.
              </span>
            </div>
            {record?.name && (
              <div
                role="group"
                aria-labelledby={`${ids}-record`}
                className="grid gap-1 rounded-lg bg-muted px-3 py-2.5"
              >
                <span
                  id={`${ids}-record`}
                  className="text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
                >
                  DNS record
                </span>
                <span className="flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-[12.5px] leading-relaxed break-all">
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
          <DialogFooter className="m-0 px-5 py-3">
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
