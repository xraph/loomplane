import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"

/**
 * useOctopusSubtitle is the line under the gateway's name: what it is, the
 * environment and the server or cluster it runs on. The service page calls
 * it on every render for this kind, always from the same place, so it may
 * read the names it needs.
 */
export function useOctopusSubtitle(
  props: CatalogPanelProps,
  environment: string
): string {
  const connections = useQuery(useConnectionsList)
  const target = connections.data?.connections.find(
    (c) => c.id === props.binding.connectionId
  )?.name
  return ["API gateway", environment, target].filter(Boolean).join(" · ")
}
