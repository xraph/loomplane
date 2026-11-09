// Nodes are machines that joined by running `loomctl node join` and dial out
// to Loomplane. These helpers turn what the API reports into the words and
// small shapes the Nodes pages show.
import type { Connection, Node } from "@/client/forge/loomplane/types"

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
})

/** clockTime is a time of day in the reader's zone, on a 24 hour clock. */
export function clockTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? "" : clock.format(d)
}

/** statusLabel is the node's state in words: "Offline since 14:02". */
export function statusLabel(n: Node): string {
  switch (n.status) {
    case "online":
      return "Online"
    case "pending":
      return "Waiting to connect"
    case "draining":
      return "Draining"
    case "revoked":
      return "Revoked"
    case "offline": {
      const at = n.lastSeenAt ? clockTime(n.lastSeenAt) : ""
      return at ? `Offline since ${at}` : "Offline"
    }
    default:
      return n.status
  }
}

/** endpointBadges are the endpoints Loomplane may use on the node. */
export function endpointBadges(n: Node): string[] {
  return [...(n.endpoints ?? [])]
}

/** machineLabel is the one-line machine summary: "linux · amd64 · 8 CPU · 32 GiB". */
export function machineLabel(n: Node): string {
  const i = n.inventory
  if (!i) return ""
  return [
    i.os,
    i.arch,
    i.cpus > 0 ? `${i.cpus} CPU` : "",
    i.memoryMib > 0 ? `${Math.round(i.memoryMib / 1024)} GiB` : "",
  ]
    .filter(Boolean)
    .join(" · ")
}

/** trafficLabel is how traffic reaches the node. */
export function trafficLabel(n: Node): string {
  // Ingress defaults to direct before the node has said what it has.
  if (n.status === "pending") return "None"
  if (n.role === "edge") return "Edge"
  switch (n.ingressMode) {
    case "direct":
      return "Direct"
    case "relay":
      return "Relayed"
    default:
      return "None"
  }
}

/** hasPublicAddress is true when the node reported an address others can reach. */
export function hasPublicAddress(n: Node): boolean {
  return (n.inventory?.publicAddresses?.length ?? 0) > 0
}

/** labelList renders a node's labels as key=value pairs. */
export function labelList(n: Node): string[] {
  return Object.entries(n.labels ?? {}).map(([k, v]) => `${k}=${String(v)}`)
}

/** parseLabels reads "region=home, tier=edge" into labels. */
export function parseLabels(
  text: string
): { ok: true; labels: Record<string, string> } | { ok: false; error: string } {
  const labels: Record<string, string> = {}
  for (const part of text.split(/[,\n]/)) {
    const pair = part.trim()
    if (!pair) continue
    const at = pair.indexOf("=")
    const key = pair.slice(0, at).trim()
    if (at < 1 || !key) {
      return {
        ok: false,
        error: `"${pair}" isn't a label. Write it as key=value.`,
      }
    }
    labels[key] = pair.slice(at + 1).trim()
  }
  return { ok: true, labels }
}

/**
 * joinedNode finds the node a join token brought in. A token makes exactly
 * one node, so it is the first live node that wasn't in the list when the
 * token was made and that this person's token created. The API names no token
 * on a node, so this is the closest match it offers.
 */
export function joinedNode(
  nodes: Node[],
  before: ReadonlySet<string>,
  userId: string
): Node | undefined {
  return nodes.find(
    (n) =>
      !before.has(n.id) &&
      n.status !== "revoked" &&
      (!n.joinedBy || !userId || n.joinedBy === userId)
  )
}

/** gpuLabel groups the GPUs by model: "1 × NVIDIA RTX 4090, 24 GiB". */
export function gpuLabel(n: Node): string {
  const groups = new Map<string, { count: number; mib: number; name: string }>()
  for (const g of n.inventory?.gpus ?? []) {
    const name = [g.vendor, g.model].filter(Boolean).join(" ")
    const key = `${name}|${g.memoryMib}`
    const cur = groups.get(key)
    if (cur) cur.count++
    else groups.set(key, { count: 1, mib: g.memoryMib, name })
  }
  return [...groups.values()]
    .map(
      (g) =>
        `${g.count} × ${g.name}${g.mib > 0 ? `, ${Math.round(g.mib / 1024)} GiB` : ""}`
    )
    .join("; ")
}

/**
 * roleNote is the plain-words line under a node's name when it has one: what
 * an edge serves, or that a Mac is for development. Labels come after it.
 */
export function roleNote(n: Node): string | undefined {
  if (n.role === "edge") {
    return n.edgeScope === "instance"
      ? "Edge for the instance"
      : "Edge for this organization"
  }
  if (n.inventory?.os === "darwin") return "macOS, for development"
  return undefined
}

/**
 * nodeIdOf is the node a server connection reaches its Docker through, or
 * undefined for any other connection.
 */
export function nodeIdOf(c: Connection | undefined): string | undefined {
  if (c?.method !== "node") return undefined
  const id = c.details?.node_id ?? c.details?.nodeId
  return typeof id === "string" && id ? id : undefined
}

/** ingressLine is how traffic reaches the node, in one sentence. */
export function ingressLine(n: Node): string {
  if (n.status === "pending") return "Not set until the node connects."
  if (n.role === "edge") return "This node is an edge."
  if (n.ingressMode !== "direct") return "None. Nothing is routed to this node."
  if (!n.publicAddress) return "Direct. Waiting for a public address."
  const at = n.publicAddressConfirmedAt
    ? clockTime(n.publicAddressConfirmedAt)
    : ""
  return at
    ? `Direct. ${n.publicAddress} answered on port 80 at ${at}.`
    : `Direct. Waiting for ${n.publicAddress} to answer on port 80.`
}
