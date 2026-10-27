// What the Octopus service page says about a gateway's board: where each
// route comes from, how its path and methods read, and the lines around
// the domain cards.
import type {
  AttachDefaults,
  Board,
  BoardRoute,
  CloudinfraRouteStatus,
  HostCard,
  OctopusRouteInputBody,
  RouteInput,
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

/** stripNote is the note the server writes for a strip route without one. */
function stripNote(route: BoardRoute): string {
  const { prefix } = splitPath(route.path)
  return route.stripPrefix && prefix !== "/"
    ? `Strips ${prefix} before forwarding`
    : ""
}

/**
 * routeDraft starts the route dialog from a row. A FARP row's note is
 * generated from its schema, so an override starts without one, and so
 * does an edit whose note is only the strip line the server fills in.
 */
export function routeDraft(
  mode: "override" | "edit",
  hostname: string,
  route: BoardRoute
): RouteDraft {
  const draft: RouteDraft = {
    hostname,
    path: route.path,
    methods: route.methods,
    service: route.service,
    port: route.port,
    stripPrefix: route.stripPrefix,
  }
  if (mode === "override") return draft
  return {
    ...draft,
    note: route.note === stripNote(route) ? "" : route.note,
    routeId: route.routeId,
    revision: route.revision,
  }
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

/**
 * farpLandings is where FARP routes land on attached hostnames: the picked
 * hostnames that are still attached, as the server pushes them to Octopus.
 * None means every hostname, unless farpStranded has some.
 */
export function farpLandings(board: Board): string[] {
  const attached = new Set(attachedHosts(board).map((h) => h.hostname))
  return board.gateway.farpHostnames.filter((h) => h && attached.has(h))
}

/**
 * farpStranded is the picked FARP hostnames when none of them is attached
 * any more, sorted. FARP routes stay on them, as the server pushes them,
 * so they answer only on the target's private network rather than on
 * every other hostname. Empty otherwise.
 */
export function farpStranded(board: Board): string[] {
  if (farpLandings(board).length) return []
  const picked = board.gateway.farpHostnames
    .filter(Boolean)
    .map((h) => h.toLowerCase())
  return [...new Set(picked)].sort()
}

/** strandedTail follows the stranded FARP hostnames in a sentence. */
export function strandedTail(count: number): string {
  return `, which ${count === 1 ? "isn't" : "aren't"} attached any more. Pick a hostname on the Domains tab.`
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
  const stranded = farpStranded(board)
  if (stranded.length)
    return {
      lead: `${watching} ${publish}. FARP routes land on `,
      hosts: stranded,
      tail: strandedTail(stranded.length),
    }
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

/**
 * exampleRequest is the route dialog's sample: a request the route would
 * answer and where Octopus sends it. A wildcard host answers as shop1, a
 * wildcard path as its /login, and an any-host route on the gateway's
 * private address.
 */
export function exampleRequest(i: {
  host: string
  path: string
  strip: boolean
  service: string
  port: number
  privateUrl: string
}): { in: string; out: string } {
  const { prefix, wildcard } = splitPath(i.path || "/")
  const sample = wildcard
    ? `${prefix === "/" ? "" : prefix}/login`
    : i.path || "/"
  const out =
    i.strip && prefix !== "/" ? sample.slice(prefix.length) || "/" : sample
  const origin = i.host
    ? `https://${i.host.replace("*", "shop1")}`
    : i.privateUrl.replace(/\/+$/, "")
  return { in: `${origin}${sample}`, out: `${i.service}:${i.port}${out}` }
}

/**
 * farpCollision is the FARP route a manual route on host and path would
 * take priority over, if FARP publishes one there.
 */
export function farpCollision(
  board: Board,
  host: string,
  path: string
): BoardRoute | undefined {
  return board.hosts
    .find((h) => h.hostname === host)
    ?.routes.find((r) => r.source === "farp" && r.path === path)
}

// The server's limit on a path, in bytes as Go counts them.
const maxPathLength = 512

/**
 * validatePath checks a path as the server's NormalizeRouteInput does and
 * says what's wrong in the same words, or nothing when it's fine.
 */
export function validatePath(path: string): string | undefined {
  if (!path.startsWith("/"))
    return "A path starts with /, like /auth or /auth/*."
  if (new TextEncoder().encode(path).length > maxPathLength)
    return `A path is at most ${maxPathLength} characters.`
  if (/[?#]/.test(path))
    return "A path can't contain ? or #. Query strings and fragments aren't part of a route."
  if (/[\s\p{Cc}]/u.test(path))
    return "A path can't contain spaces or control characters."
  if (path.includes("//")) return "A path can't contain //."
  if (path !== "/" && path.endsWith("/"))
    return "Drop the trailing slash, or end with /* to match everything under it."
  const segments = path.slice(1).split("/")
  for (const [i, seg] of segments.entries()) {
    if (seg === "." || seg === "..")
      return "A path can't contain . or .. segments."
    if (seg.includes("*") && (seg !== "*" || i !== segments.length - 1))
      return "A * is only allowed as the last segment, as /*."
  }
  return undefined
}

/** The only port a Loomplane route to Octopus ever sends to. */
export const gatewayPort = 8080

/**
 * gatewayRouteInput is the domain route that attaches hostname to the
 * gateway: always its port 8080, with the admin and FARP paths blocked.
 */
export function gatewayRouteInput(
  defaults: AttachDefaults,
  hostname: { zoneId: string; hostname: string }
): RouteInput {
  return {
    zoneId: hostname.zoneId,
    cloudId: defaults.cloudId ?? "",
    environmentId: defaults.environmentId,
    service: defaults.service,
    hostname: hostname.hostname,
    port: gatewayPort,
    blockedPathPrefixes: defaults.blockedPathPrefixes,
  }
}

/** attachedHosts are the board's cards for hostnames with a domain route. */
export function attachedHosts(board: Board): HostCard[] {
  return board.hosts.filter((h) => h.hostname && h.attached)
}

/** manualRoutes counts the routes added here on a card, which hold it. */
export function manualRoutes(host: HostCard): number {
  return host.routes.filter((r) => r.source === "here").length
}

/**
 * farpLanding is whether FARP routes land on an attached hostname: it's
 * picked, or nothing is and they land everywhere. While every picked
 * hostname is detached they land on none of the attached ones.
 */
export function farpLanding(board: Board, hostname: string): boolean {
  const landings = farpLandings(board)
  if (landings.length) return landings.includes(hostname)
  return farpStranded(board).length === 0
}

/**
 * farpLandingCount is how many attached hostnames FARP routes land on:
 * every one of them while none is picked, and none while every picked one
 * is detached.
 */
export function farpLandingCount(board: Board): number {
  if (farpStranded(board).length) return 0
  return farpLandings(board).length || attachedHosts(board).length
}

/**
 * farpHostnamesWith is where FARP routes land once hostname is turned on
 * or off. While none is picked they land on every attached hostname, so
 * turning one off picks all the others; it never widens to every hostname.
 * While every picked one is detached, turning one on picks it alone.
 */
export function farpHostnamesWith(
  board: Board,
  hostname: string,
  on: boolean
): string[] {
  const landings = farpLandings(board)
  const base = landings.length
    ? landings
    : farpStranded(board).length
      ? []
      : attachedHosts(board).map((h) => h.hostname)
  const kept = base.filter((h) => h !== hostname)
  return on ? [...kept, hostname] : kept
}

/** What a discovered service's state pill says, and its tone. */
export function discoveryState(state: string): {
  label: string
  tone: "overdye" | "weld" | "failed"
} {
  switch (state) {
    case "registered":
      return { label: "Registered", tone: "overdye" }
    case "discovered":
      return { label: "Discovered", tone: "overdye" }
    case "failed":
      return { label: "Failed", tone: "failed" }
  }
  return { label: "Waiting for a release", tone: "weld" }
}

// The metadata keys that name a schema, in the order they show.
const schemaKeys = [
  "farp.openapi.path",
  "farp.asyncapi.path",
  "farp.graphql.path",
] as const

/**
 * farpSchemas are the schema paths a service publishes, from its FARP
 * metadata, and gRPC reflection when it's on.
 */
export function farpSchemas(metadata: Record<string, unknown> = {}): string[] {
  const paths = schemaKeys
    .map((k) => metadata[k])
    .filter((v): v is string => typeof v === "string" && v !== "")
  if (metadata["farp.grpc.reflection"] === "true") paths.push("gRPC reflection")
  return paths
}

/** The opt-in a service adds to the manifest, as the Discovery tab shows it. */
export const farpExample = `services:
  billing:
    port: 8080
    farp:
      openapi: /openapi.json
      capabilities: [rest, websocket]`
