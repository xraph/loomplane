import { ExternalLinkIcon } from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useCatalogAdminOpen } from "@/client/forge/loomplane/hooks"
import type { CatalogproxySession } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { useOrganization } from "@/lib/organization"

/**
 * OpenAdminButton is the service page's Open Octopus admin. It opens the
 * admin through Loomplane's proxy, already signed in, so nobody ever sees
 * the admin password.
 */
export function OpenAdminButton({
  projectId,
  environmentId,
  resource,
}: CatalogPanelProps) {
  const { can } = useOrganization()
  const session = useMutation(useCatalogAdminOpen)

  const open = () => {
    // The tab opens in the click itself, before the session is made, or a
    // popup blocker would stop it. It points at the admin once the URL is
    // back, and can't reach back into this page.
    const tab = window.open("about:blank", "_blank")
    if (tab) tab.opener = null
    session
      .mutateAsync({ path: { projectId, environmentId, resource } })
      .then((made) => {
        // The generated mutation doesn't carry the response's type.
        const url = (made as CatalogproxySession).url
        if (tab) tab.location.href = url
        else window.open(url, "_blank", "noopener,noreferrer")
      })
      .catch((cause: unknown) => {
        tab?.close()
        toast.error(messageOf(cause, "Octopus's admin couldn't be opened."))
      })
  }

  const button = (
    <Button
      type="button"
      variant="outline"
      disabled={!can("infrastructure:modify") || session.isPending}
      onClick={open}
    >
      Open Octopus admin
      <ExternalLinkIcon data-icon="inline-end" aria-hidden />
    </Button>
  )
  if (can("infrastructure:modify")) return button
  // A disabled button gets no pointer events, so the reason hangs on a
  // wrapper around it.
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="inline-flex" tabIndex={0} />}>
        {button}
      </TooltipTrigger>
      <TooltipContent>
        You need permission to change this project&apos;s infrastructure.
      </TooltipContent>
    </Tooltip>
  )
}
