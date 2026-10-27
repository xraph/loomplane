// The Routes tab: a gateway's routes in one card per domain, with what
// FARP is watching above them and the services nothing routes to below.
import { LockIcon } from "lucide-react"
import { useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useOctopusBoard,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type { Board, BoardRoute } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DiscoveryStrip } from "@/components/octopus/discovery-strip"
import { HostCardView } from "@/components/octopus/host-card"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import {
  filterBoard,
  unroutedParts,
  type RouteDraft,
  type RouteDialogMode,
  type SourceFilter,
} from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"

/**
 * useRouteDialog is what the add, edit and override dialog opens with:
 * the mode and the route to start from. Nothing draws it yet.
 */
function useRouteDialog() {
  const [request, setRequest] = useState<{
    mode: RouteDialogMode
    initial: RouteDraft
  } | null>(null)
  return {
    request,
    open: (mode: RouteDialogMode, initial: RouteDraft = {}) =>
      setRequest({ mode, initial }),
    close: () => setRequest(null),
  }
}

/** draftOf starts a dialog from a row on a host. */
function draftOf(hostname: string, route: BoardRoute): RouteDraft {
  return {
    hostname,
    path: route.path,
    methods: route.methods,
    service: route.service,
    port: route.port,
    stripPrefix: route.stripPrefix,
    note: route.note,
  }
}

export function RoutesPanel(props: CatalogPanelProps) {
  const { projectId, environmentId, resource } = props
  const board = useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  })

  if (board.status === "error" && !board.data) {
    const status = statusOf(board.error)
    if (status === 401 || status === 403) return <Denied />
    return (
      <QueryError
        error={board.error}
        fallback="Octopus's routes couldn't be loaded."
        retry={board.refetch}
      />
    )
  }
  if (!board.data) return <Loading />
  return (
    <div className="grid min-w-0 gap-3.5">
      {/* A refresh that fails keeps what was read before on screen. */}
      {board.status === "error" && (
        <QueryError
          error={board.error}
          fallback="Octopus's routes couldn't be loaded."
          retry={board.refetch}
        />
      )}
      <Routes {...props} board={board.data} />
    </div>
  )
}

function Routes({
  projectId,
  environmentId,
  board,
}: CatalogPanelProps & { board: Board }) {
  const { can } = useOrganization()
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const [source, setSource] = useState<SourceFilter>("all")
  const dialog = useRouteDialog()
  // The add, edit and override dialog opens from dialog.request.
  const writes = can("infrastructure:modify")

  const environment =
    environments.data?.environments.find((e) => e.id === environmentId)?.name ??
    environmentId
  const hosts = filterBoard(board, source)
  const unrouted = unroutedParts(board)
  const firstUnrouted = board.unrouted[0]

  return (
    <>
      <DiscoveryStrip
        board={board}
        project={project.data?.name ?? projectId}
        environment={environment}
        source={source}
        onSource={setSource}
        onAdd={writes ? () => dialog.open("add") : undefined}
      />

      {hosts.map((host) => (
        <HostCardView
          key={host.hostname}
          host={host}
          privateUrl={board.gateway.privateUrl}
          onRoute={
            writes
              ? () => dialog.open("add", { hostname: host.hostname })
              : undefined
          }
          onRow={
            writes
              ? (kind, route) =>
                  dialog.open(
                    kind,
                    kind === "edit"
                      ? {
                          ...draftOf(host.hostname, route),
                          routeId: route.routeId,
                          revision: route.revision,
                        }
                      : draftOf(host.hostname, route)
                  )
              : undefined
          }
        />
      ))}

      {hosts.length === 0 &&
        (source === "all" ? (
          <ZeroState>
            <ZeroStateHeader>
              <ZeroStateTitle>No routes yet</ZeroStateTitle>
              <ZeroStateDescription>
                Add a route by domain and path, or add a farp block to a service
                in the manifest so Octopus discovers its routes.
              </ZeroStateDescription>
            </ZeroStateHeader>
          </ZeroState>
        ) : (
          <ZeroState>
            <ZeroStateHeader>
              <ZeroStateTitle>No routes from this source</ZeroStateTitle>
              <ZeroStateDescription>
                Pick All to see every route on this gateway.
              </ZeroStateDescription>
            </ZeroStateHeader>
          </ZeroState>
        ))}

      {unrouted.length > 0 && (
        <section
          aria-label="Services without a route"
          className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[14px] border border-dashed border-border px-4 py-3"
        >
          <span className="flex-[1_1_360px] text-[13px] leading-normal text-foreground/80">
            {unrouted.map((part, i) => (
              <span key={part.service}>
                {i > 0 && " "}
                <b className="font-semibold text-foreground">
                  {part.service}
                </b>{" "}
                {part.port !== undefined && (
                  <>
                    <span className="font-mono text-xs text-muted-foreground">
                      :{part.port}
                    </span>{" "}
                  </>
                )}
                {part.text}
              </span>
            ))}
          </span>
          {writes && firstUnrouted && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                dialog.open("add", {
                  service: firstUnrouted.service,
                  port: firstUnrouted.port,
                })
              }
            >
              Route {firstUnrouted.service}
            </Button>
          )}
        </section>
      )}
    </>
  )
}

function Loading() {
  return (
    <div aria-busy="true" aria-label="Loading routes" className="grid gap-3.5">
      <Skeleton className="h-[58px] rounded-[14px]" />
      <Skeleton className="h-40 rounded-[14px]" />
      <Skeleton className="h-28 rounded-[14px]" />
    </div>
  )
}

// A missing permission isn't a failure to retry, so it reads differently.
function Denied() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>
          You can&apos;t see this gateway&apos;s routes
        </ZeroStateTitle>
        <ZeroStateDescription>
          Your role doesn&apos;t include reading this project&apos;s
          infrastructure. An organization owner can give you access.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}
