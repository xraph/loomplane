import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"

/**
 * useFabriqSubtitle is the line under the resource's name: where this one
 * runs. The service page calls it on every render for this kind, always
 * from the same place, so it may read the names it needs.
 */
export function useFabriqSubtitle(
  props: CatalogPanelProps,
  environment: string
): string {
  const connections = useQuery(useConnectionsList)
  const target = connections.data?.connections.find(
    (c) => c.id === props.binding.connectionId
  )?.name
  return [
    props.kind === "fabriq-library" ? "Fabriq library" : "Hosted Fabriq server",
    environment,
    target,
  ]
    .filter(Boolean)
    .join(" · ")
}
