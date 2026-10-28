import { buttonVariants } from "@/components/ui/button"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { Link } from "@/lib/router"

/**
 * OpenModelPage is the link on a model resource's service page: what a
 * model costs, who links to it and how to call it are on its model page.
 * It's a link that looks like a button, so it reads as one.
 */
export function OpenModelPage({
  projectId,
  environmentId,
  resource,
}: CatalogPanelProps) {
  return (
    <Link
      to="/projects/$id/ai/models/$resource"
      params={{ id: projectId, resource }}
      search={{ environment: environmentId }}
      className={buttonVariants({ variant: "outline", size: "sm" })}
    >
      Open the model page
    </Link>
  )
}
