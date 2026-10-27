// The Routes tab: a gateway's routes in one card per domain, with what
// FARP is watching above them and the services nothing routes to below.
import { useState } from "react"

import {
  useEnvironmentsList,
  useOctopusBoard,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type { Board } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DiscoveryStrip } from "@/components/octopus/discovery-strip"
import { HostCardView } from "@/components/octopus/host-card"
import { Panel } from "@/components/panel"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
import { RouteDialog } from "@/components/octopus/route-dialog"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import {
  filterBoard,
  routeDraft,
  unroutedParts,
  type RouteDraft,
  type RouteDialogMode,
  type SourceFilter,
} from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"

/**
 * useRouteDialog is what the add, edit and override dialog opens with:
 * the mode and the route to start from. Each opening gets its own key so
 * the dialog starts fresh.
 */
function useRouteDialog() {
  const [request, setRequest] = useState<{
    key: number
    mode: RouteDialogMode
    initial: RouteDraft
  } | null>(null)
  return {
    request,
    open: (mode: RouteDialogMode, initial: RouteDraft = {}) =>
      setRequest((prev) => ({ key: (prev?.key ?? 0) + 1, mode, initial })),
    close: () => setRequest(null),
  }
}

export function RoutesPanel(props: CatalogPanelProps) {
  const { projectId, environmentId, resource } = props
  const board = useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  })

  if (board.status === "error" && !board.data)
    return (
      <ReadFailed
        error={board.error}
        retry={board.refetch}
        denied="You can't see this gateway's routes"
        fallback="Octopus's routes couldn't be loaded."
      />
    )
  if (!board.data) return <PanelLoading label="Loading routes" />
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
  resource,
  board,
}: CatalogPanelProps & { board: Board }) {
  const { can } = useOrganization()
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const [source, setSource] = useState<SourceFilter>("all")
  const dialog = useRouteDialog()
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
                  dialog.open(kind, routeDraft(kind, host.hostname, route))
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
        <Panel
          dashed
          aria-label="Services without a route"
          className="flex-row flex-wrap items-center gap-x-4 gap-y-2.5 py-3"
        >
          <span className="flex-[1_1_360px] text-[13px] text-muted-foreground">
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
        </Panel>
      )}

      {writes && dialog.request && (
        <RouteDialog
          key={dialog.request.key}
          projectId={projectId}
          environmentId={environmentId}
          resource={resource}
          mode={dialog.request.mode}
          initial={dialog.request.initial}
          open
          onOpenChange={(open) => {
            if (!open) dialog.close()
          }}
        />
      )}
    </>
  )
}
