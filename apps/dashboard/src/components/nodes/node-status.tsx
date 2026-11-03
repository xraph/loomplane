import type { Node } from "@/client/forge/loomplane/types"
import { StatusPill, type Tone } from "@/components/runs"
import { statusLabel } from "@/lib/nodes"

const tones: Record<string, Tone> = {
  online: "live",
  offline: "failed",
  pending: "waiting",
  draining: "waiting",
  revoked: "quiet",
}

/** NodeStatus is a node's state as a pill: a dot and the words, never color alone. */
export function NodeStatus({ node }: { node: Node }) {
  return (
    <StatusPill tone={tones[node.status] ?? "quiet"}>
      {statusLabel(node)}
    </StatusPill>
  )
}
