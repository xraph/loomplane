import { ExternalLinkIcon } from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useCatalogAdminOpen } from "@/client/forge/loomplane/hooks"
import type { CatalogproxySession } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import { useOrganization } from "@/lib/organization"

type Target = { projectId: string; environmentId: string; resource: string }

/**
 * useOpenAdmin opens Fabriq's admin console through Loomplane's proxy, in a
 * new tab already signed in. The console opens on its own default tenant.
 */
function useOpenAdmin({ projectId, environmentId, resource }: Target) {
  const open = useMutation(useCatalogAdminOpen)
  return {
    pending: open.isPending,
    open: async () => {
      try {
        // The generated mutation doesn't carry the response's type.
        const session = (await open.mutateAsync({
          path: { projectId, environmentId, resource },
        })) as CatalogproxySession
        window.open(session.url, "_blank", "noopener,noreferrer")
      } catch (cause) {
        toast.error(messageOf(cause, "Fabriq's admin couldn't be opened."))
      }
    },
  }
}

/** OpenAdminButton is the service page's Open Fabriq admin. */
export function OpenAdminButton(props: Target) {
  const { can } = useOrganization()
  const admin = useOpenAdmin(props)
  if (!can("infrastructure:modify")) return null
  return (
    <Button
      type="button"
      variant="outline"
      className="gap-[7px] px-3 has-data-[icon=inline-end]:pr-3 [&_svg]:size-3.5"
      disabled={admin.pending}
      onClick={() => void admin.open()}
    >
      Open Fabriq admin
      <ExternalLinkIcon data-icon="inline-end" aria-hidden />
    </Button>
  )
}

/** OpenInAdmin is a table row's Open in admin. */
export function OpenInAdmin(props: Target) {
  const { can } = useOrganization()
  const admin = useOpenAdmin(props)
  if (!can("infrastructure:modify")) return null
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="h-7 rounded-lg text-xs"
      disabled={admin.pending}
      onClick={() => void admin.open()}
    >
      Open in admin
    </Button>
  )
}
