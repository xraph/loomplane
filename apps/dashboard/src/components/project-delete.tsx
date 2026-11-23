// Deleting a project: what still runs from it, the danger zone on its
// Settings, and the dialog that asks for its name first.
import { useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useBindingsList,
  useProjectsDelete,
  useProjectsDeletion,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import {
  CloudCallout,
  CloudConfirm,
} from "@/components/cloud-targets/cloud-page"
import { CloudSectionHead } from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import { useNavigate } from "@/lib/router"

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/**
 * useDeployed counts what runs from a project across its environments, so
 * deleting it says what is left running.
 */
function useDeployed(project: Project, environments?: Environment[]) {
  const list = useMemo(() => environments ?? [], [environments])
  const args = useMemo(
    () =>
      list.map((e) => ({
        path: { id: project.id },
        query: { environment: e.name },
      })),
    [list, project.id]
  )
  const { states } = useQueries(useBindingsList, args)
  let services = 0
  let resources = 0
  let places = 0
  for (const s of states) {
    const bindings = s?.data?.bindings ?? []
    const svc = bindings.filter((b) => b.subject === "service").length
    services += svc
    resources += bindings.length - svc
    if (bindings.length > 0) places++
  }
  return {
    loaded: !!environments && states.every((s) => s?.status === "success"),
    failed: states.some((s) => s?.status === "error"),
    services,
    resources,
    places,
  }
}

/**
 * deployedSummary says what runs from a project, for its delete copy.
 * removes is how many frontends the deletion removes from their provider
 * first, when that's known: until then, "nothing runs" can't promise that
 * no cloud resource is touched.
 */
function deployedSummary(
  d: ReturnType<typeof useDeployed>,
  removes?: number
): string {
  if (d.failed)
    return "What runs from it couldn't all be read. Anything deployed keeps running until you remove it in its account."
  if (!d.loaded) return "Checking what runs from it."
  if (d.services + d.resources === 0) {
    if (removes === 0)
      return "Nothing runs from it yet, so no cloud resources are touched."
    return "Nothing runs from it yet."
  }
  const what = [
    d.services > 0 && plural(d.services, "service"),
    d.resources > 0 && plural(d.resources, "resource"),
  ]
    .filter(Boolean)
    .join(" and ")
  return `${what} ${d.services + d.resources === 1 ? "runs" : "run"} in ${plural(d.places, "environment")}. Deleting stops managing them; what runs keeps running until you remove it in each account.`
}

export function DangerZone({
  project,
  environments,
  onDelete,
}: {
  project: Project
  environments?: Environment[]
  onDelete: () => void
}) {
  const deployed = useDeployed(project, environments)
  return (
    <section aria-label="Danger zone" className="grid gap-2.5">
      <CloudSectionHead
        title="Danger zone"
        description="Each asks you to confirm in a dialog before anything happens."
      />
      <div className="flex flex-wrap items-center gap-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/25">
        <div className="grid min-w-0 flex-[1_1_360px] gap-1">
          <p className="text-[13px] font-semibold">Delete this project</p>
          <p className="text-[12.5px] leading-normal text-muted-foreground">
            {deployedSummary(deployed)}
          </p>
        </div>
        <Button
          variant="outline"
          className="border-destructive/25 text-destructive hover:bg-destructive/10 hover:text-destructive"
          onClick={onDelete}
        >
          Delete project
        </Button>
      </div>
    </section>
  )
}

export function DeleteProject({
  project,
  environments,
  open,
  onOpenChange,
}: {
  project: Project
  environments?: Environment[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const remove = useMutation(useProjectsDelete)
  const deployed = useDeployed(project, open ? environments : undefined)
  const [error, setError] = useState("")

  const confirm = async () => {
    setError("")
    try {
      // The delete invalidates the project list for us.
      await remove.mutateAsync({ path: { id: project.id } })
      toast.success(`Deleted ${project.name}`)
      onOpenChange(false)
      void navigate({ to: "/projects", replace: true })
    } catch (err) {
      setError(messageOf(err, "The project wasn't deleted."))
    }
  }

  return (
    <CloudConfirm
      open={open}
      onOpenChange={(next) => {
        if (!next) setError("")
        onOpenChange(next)
      }}
      title={`Delete ${project.name}?`}
      confirmLabel="Delete project"
      typeToConfirm={project.name}
      pending={remove.isPending}
      onConfirm={() => void confirm()}
      details={<DeletionRemoves projectId={project.id} />}
      error={error && <CloudCallout tone="error" title={error} />}
    >
      Its environments and their variables go with it, and this can&apos;t be
      undone. <DeletedSummary projectId={project.id} deployed={deployed} />
    </CloudConfirm>
  )
}

/**
 * useDeletionPreview asks the server what deleting the project would
 * remove from provider accounts first, and whether it would be refused.
 * It reads those accounts, so only the open delete dialog asks, and asks
 * again each time it opens.
 */
function useDeletionPreview(projectId: string) {
  return useQuery(
    useProjectsDeletion,
    { path: { id: projectId } },
    { staleTime: 0 }
  )
}

function DeletedSummary({
  projectId,
  deployed,
}: {
  projectId: string
  deployed: ReturnType<typeof useDeployed>
}) {
  const preview = useDeletionPreview(projectId)
  return deployedSummary(deployed, preview.data?.removes.length)
}

/**
 * DeletionRemoves names the frontends Loomplane deletes from their
 * provider before the project goes, and says up front when the deletion
 * would be refused, so nobody types the name only to be told no.
 */
function DeletionRemoves({ projectId }: { projectId: string }) {
  const { data } = useDeletionPreview(projectId)
  if (!data) return null
  if (data.blocked)
    return (
      <CloudCallout title="Deleting it now would be refused">
        {data.blocked}
      </CloudCallout>
    )
  if (data.removes.length === 0) return null
  return (
    <CloudCallout
      tone="info"
      title={`Loomplane deletes ${data.removes.length === 1 ? "this frontend" : "these frontends"} from Vercel first`}
    >
      <ul className="list-disc pl-4">
        {data.removes.map((r) => (
          <li key={r}>{r}</li>
        ))}
      </ul>
    </CloudCallout>
  )
}
