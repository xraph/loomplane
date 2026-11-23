// One "Add service" dialog per project page, which anything on the page
// can open on an environment and a tab: the header, the Services tab, an
// environment's row, or an empty state that used to say "edit
// loomplane.yaml".
import { PlusIcon, SparklesIcon } from "lucide-react"
import { createContext, type ReactNode, useContext, useState } from "react"

import {
  useManifestsCurrent,
  useManifestsSuggestions,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  AddServiceDialog,
  type AddServiceStart,
} from "@/components/add-service-dialog"
import { CloudCallout } from "@/components/cloud-targets/cloud-page"
import { Button } from "@/components/ui/button"

/** Opens the dialog, on an environment and a tab when given. */
export type OpenAddService = (options?: {
  environment?: string
  start?: AddServiceStart
}) => void

const AddServiceContext = createContext<OpenAddService | null>(null)

/**
 * useAddService opens the project's Add service dialog, or is null outside
 * a project page or for someone who can't change the project, so a caller
 * shows its button only when it works.
 */
export function useAddService(): OpenAddService | null {
  return useContext(AddServiceContext)
}

export function AddServiceProvider({
  project,
  environments,
  enabled,
  children,
}: {
  project: Project
  environments: Environment[]
  /** Whether the person may change the project. */
  enabled: boolean
  children: ReactNode
}) {
  const [state, setState] = useState<{
    open: boolean
    environment?: string
    start?: AddServiceStart
  }>({ open: false })
  const open: OpenAddService = (options) => setState({ open: true, ...options })
  return (
    <AddServiceContext.Provider value={enabled ? open : null}>
      {children}
      {enabled && (
        <AddServiceDialog
          project={project}
          environments={environments}
          environment={state.environment}
          start={state.start}
          open={state.open}
          onOpenChange={(next) => setState((s) => ({ ...s, open: next }))}
        />
      )}
    </AddServiceContext.Provider>
  )
}

/**
 * AddServiceButton opens the dialog, and renders nothing where the dialog
 * isn't available.
 */
export function AddServiceButton({
  environment,
  start,
  size,
  variant = "outline",
  children = "Add service",
}: {
  environment?: string
  start?: AddServiceStart
  size?: "sm" | "default"
  variant?: "outline" | "default"
  children?: ReactNode
}) {
  const open = useAddService()
  if (!open) return null
  return (
    <Button
      size={size}
      variant={variant}
      onClick={() => open({ environment, start })}
    >
      <PlusIcon />
      {children}
    </Button>
  )
}

/**
 * FoundServices says when analysis found services the saved manifest
 * doesn't run, and offers to add them. A project without a saved manifest
 * gets the drafted one on its Deployments tab instead.
 */
export function FoundServices({ projectId }: { projectId: string }) {
  const found = useFoundServices(projectId)
  if (!found) return null
  return (
    <CloudCallout
      icon={SparklesIcon}
      title={found.title}
      action={
        <Button size="sm" variant="outline" onClick={found.open}>
          Review and add
        </Button>
      }
    >
      {found.detail}
    </CloudCallout>
  )
}

/**
 * useFoundServices is what FoundServices says, for a page that lists it
 * among other things: null when there's nothing to add, or nobody here
 * may add it.
 */
export function useFoundServices(projectId: string): {
  names: string[]
  title: string
  detail: string
  open: () => void
} | null {
  const open = useAddService()
  const current = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const found = useQuery(useManifestsSuggestions, { path: { id: projectId } })
  const services = found.data?.services ?? []
  if (!open || !current.data || services.length === 0) return null
  const names = services.map((s) => s.service.name)
  const listed =
    names.length <= 3
      ? names.join(", ")
      : `${names.slice(0, 3).join(", ")} and ${names.length - 3} more`
  return {
    names,
    title: `Analysis found ${names.length === 1 ? "a service" : `${names.length} services`} that ${names.length === 1 ? "isn't" : "aren't"} in loomplane.yaml`,
    detail: `${listed}. Nothing deploys them until they're added and planned.`,
    open: () => open(),
  }
}
