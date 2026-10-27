// What the Octopus service page says about a gateway's board: where each
// route comes from, how its path and methods read, and the lines around
// the domain cards.
import type {
  Board,
  BoardRoute,
  CloudinfraRouteStatus,
  HostCard,
  OctopusRouteInputBody,
} from "@/client/forge/loomplane/types"
import {
  certificateLabel,
  dnsLabel,
  dnsTone,
  stateTone,
  type DomainState,
  type PillTone,
} from "@/lib/domains"

/** Where a board row comes from: BoardRoute.source. */
export type RouteSource = "farp" | "here" | "octopus-admin"

/** How the route dialog opens: a new route, one added here, or a FARP one. */
export type RouteDialogMode = "add" | "edit" | "override"

/** What the route dialog starts from. */
export type RouteDraft = Partial<OctopusRouteInputBody> & {
  routeId?: string
  revision?: number
}

/** The Routes tab's source filter. */
export type SourceFilter = "all" | "farp" | "here"

/**
 * splitPath reads an Octopus path: "/auth/*" is everything under /auth,
 * "/*" everything, and a path without the star only itself.
 */
export function splitPath(path: string): { prefix: string; wildcard: boolean } {
  if (!path.endsWith("/*")) return { prefix: path, wildcard: false }
  return { prefix: path.slice(0, -2) || "/", wildcard: true }
}

const sourceLabels: Record<
  RouteSource,
  "FARP" | "Added here" | "Octopus admin"
> = {
  farp: "FARP",
  here: "Added here",
  "octopus-admin": "Octopus admin",
}

export function routeSourceLabel(
  source: RouteSource
): "FARP" | "Added here" | "Octopus admin" {
  return sourceLabels[source]
}

/** methodsLabel is "Any" for a route that takes every method. */
export function methodsLabel(methods: string[]): string {
  return methods.length === 0 ? "Any" : methods.join(" ")
}

/** routeCountLabel is a card's "1 route" or "4 routes". */
export function routeCountLabel(n: number): string {
  return n === 1 ? "1 route" : `${n} routes`
}

/**
 * routeNote is the line under a row's path: its note, then what it strips
 * before forwarding.
 */
export function routeNote(route: BoardRoute): string {
  const { prefix } = splitPath(route.path)
  const strips =
    route.stripPrefix && prefix !== "/"
      ? `Strips ${prefix} before forwarding`
      : ""
  return [route.note, strips].filter(Boolean).join(" · ")
}

/**
 * filterBoard keeps the rows from one source, or all of them, and drops
 * the cards left with none: an any-host card with no FARP routes yet
 * doesn't show.
 */
export function filterBoard(board: Board, source: SourceFilter): HostCard[] {
  return board.hosts
    .map((host) => ({
      ...host,
      routes: host.routes.filter(
        (r) => source === "all" || r.source === source
      ),
    }))
    .filter((host) => host.routes.length > 0)
}

/** farpLandings is where FARP routes land, or none for every hostname. */
function farpLandings(board: Board): string[] {
  return board.gateway.farpHostnames.filter(Boolean)
}

/**
 * discoverySentence is the strip's line: what FARP watches, how many
 * services publish schemas and where their routes land. The hosts go
 * between lead and tail, set in mono.
 */
export function discoverySentence(
  board: Board,
  project: string,
  environment: string
): { lead: string; hosts: string[]; tail: string } {
  const watching = `FARP is watching ${project} / ${environment}.`
  const n = board.discovery.services.filter((s) => s.optedIn).length
  if (n === 0)
    return {
      lead: `${watching} No service publishes FARP schemas yet. Add a farp block to a service in the manifest to have Octopus discover its routes.`,
      hosts: [],
      tail: "",
    }
  const publish =
    n === 1 ? "1 service publishes schemas" : `${n} services publish schemas`
  const hosts = farpLandings(board)
  if (hosts.length === 0)
    return {
      lead: `${watching} ${publish}, and their routes answer on every hostname until you pick where they land.`,
      hosts: [],
      tail: "",
    }
  return {
    lead: `${watching} ${publish}, and their routes land on `,
    hosts,
    tail: ".",
  }
}

/** One service the unrouted line names. */
export type UnroutedPart = { service: string; port?: number; text: string }

/**
 * unroutedParts are the services nothing routes to: the ones with an HTTP
 * port first, then the ones that can't take a route and why.
 */
export function unroutedParts(board: Board): UnroutedPart[] {
  return [
    ...board.unrouted.map((s) => ({
      service: s.service,
      port: s.port,
      text: "has no route yet.",
    })),
    ...board.unroutable.map((s) => ({
      service: s.service,
      text:
        s.reason === "other-target"
          ? "runs on a different server or cluster, so Octopus can't reach it."
          : "has no HTTP port, so it can't take one.",
    })),
  ]
}

/** unroutedSentence is the unrouted line as plain text. */
export function unroutedSentence(board: Board): string {
  return unroutedParts(board)
    .map((p) =>
      [p.service, p.port !== undefined && `:${p.port}`, p.text]
        .filter(Boolean)
        .join(" ")
    )
    .join(" ")
}

/**
 * hostPills are a domain card's DNS and certificate pills: the Domains
 * page's states, worded as the Routes mock words them.
 */
export function hostPills(status: CloudinfraRouteStatus): [string, PillTone][] {
  const dns =
    status.dns === "ready" ? "DNS verified" : `DNS ${lower(dnsLabel(status))}`
  const certificate =
    status.certificate === "ready"
      ? "Certificate active"
      : status.certificate === "provisioning"
        ? "Certificate pending"
        : `Certificate ${lower(certificateLabel(status))}`
  return [
    [dns, dnsTone(status)],
    [certificate, stateTone(status.certificate as DomainState)],
  ]
}

function lower(s: string): string {
  return s.charAt(0).toLowerCase() + s.slice(1)
}
