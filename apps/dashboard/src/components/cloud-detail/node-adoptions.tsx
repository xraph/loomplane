// Nodes that joined this Cloud and haven't become its servers yet. Most adopt
// by themselves; the ones listed here need a person, and Adopt starts the same
// reviewed adopt every connected server goes through.
import { useState } from "react"

import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  NodeAdoption,
  Profile,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudCallout } from "@/components/cloud-targets/cloud-page"
import { ServerEditor } from "@/components/cloud-targets/servers"
import { Button } from "@/components/ui/button"
import { nodeIdOf } from "@/lib/nodes"

export function NodeAdoptions({
  cloud,
  connections,
  onChanged,
}: {
  cloud: Profile
  connections: Connection[]
  onChanged: () => void
}) {
  const adoptions = useQuery(api.useCloudNodeAdoptionsList, {
    path: { cloudId: cloud.id },
  })
  const [adopting, setAdopting] = useState<NodeAdoption>()
  // An applied adoption is a server already, and it shows in Servers.
  const open = (adoptions.data?.adoptions ?? []).filter(
    (a): a is NodeAdoption =>
      !!a && ["waiting", "review", "failed"].includes(a.status)
  )
  if (open.length === 0) return null
  const connectionOf = (a: NodeAdoption) =>
    connections.find((c) => nodeIdOf(c) === a.nodeId && c.status === "ready")

  return (
    <div className="grid gap-2" aria-label="Nodes waiting to join this Cloud">
      {open.map((a) => {
        const connection = connectionOf(a)
        return (
          <CloudCallout
            key={a.nodeId}
            tone={
              a.status === "failed"
                ? "error"
                : a.status === "waiting"
                  ? "info"
                  : "attention"
            }
            title={a.nodeName}
            action={
              a.status !== "waiting" && connection ? (
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Adopt ${a.nodeName}`}
                  onClick={() => setAdopting(a)}
                >
                  Adopt
                </Button>
              ) : undefined
            }
          >
            {a.status === "waiting" ? (
              <span>Waiting for the node</span>
            ) : (
              <span>
                {a.reason ||
                  (a.status === "failed"
                    ? "Joining this Cloud failed."
                    : "This node needs a review before it joins this Cloud.")}
              </span>
            )}
          </CloudCallout>
        )
      })}
      {adopting && (
        <ServerEditor
          cloud={cloud}
          kind="adopt"
          initial={{
            connectionId: connectionOf(adopting)?.id,
            name: adopting.nodeName,
          }}
          providers={[]}
          connections={connections}
          onClose={() => setAdopting(undefined)}
          onChanged={() => {
            void adoptions.refetch()
            onChanged()
          }}
        />
      )}
    </div>
  )
}
