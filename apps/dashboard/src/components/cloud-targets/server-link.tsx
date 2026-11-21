import type { Server } from "@/client/forge/loomplane/types"
import { Link } from "@/lib/router"

export function ServerLink({ server }: { server: Server }) {
  return (
    <Link
      to="/clouds/$id"
      params={{ id: server.cloudId }}
      search={{ tab: "infrastructure", view: "servers", server: server.id }}
      className="truncate text-[13px] font-medium hover:underline"
    >
      {server.name}
    </Link>
  )
}
