import { UsersRoundIcon } from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useProjectsSetTeam,
  useTeamsList,
} from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useProjectCan } from "@/lib/project-permissions"

// Select values can't be empty, so "no team" gets a name of its own.
const none = "none"

/**
 * ProjectTeam names the team the agent sends a project's requests and
 * notices to. Anyone who may update the project may change it.
 */
export function ProjectTeam({ project }: { project: Project }) {
  const can = useProjectCan(project.id)
  const teams = useQuery(useTeamsList)
  const setTeam = useMutation(useProjectsSetTeam)
  const list = teams.data?.teams ?? []
  const mayEdit = can("project:update")
  const current = project.responsibleTeamId || none

  const choose = async (value: string | null) => {
    const next = value && value !== none ? value : null
    if ((next ?? none) === current) return
    try {
      await setTeam.mutateAsync({
        path: { id: project.id },
        body: { teamId: next },
      })
      toast.success(
        next
          ? `${list.find((t) => t.id === next)?.name ?? "The team"} is now responsible`
          : "No team is responsible now"
      )
    } catch (err) {
      toast.error(messageOf(err, "The team wasn't changed."))
    }
  }

  const items = [
    { label: "None, workspace admins get its requests", value: none },
    ...list.map((t) => ({ label: t.name, value: t.id })),
  ]

  return (
    <section
      aria-labelledby="project-team"
      className="grid gap-2 rounded-xl bg-card px-4 py-3.5 ring-1 ring-border"
    >
      <h2
        id="project-team"
        className="flex items-center gap-2 text-[14px] font-semibold"
      >
        <UsersRoundIcon className="size-4 text-muted-foreground" />
        Responsible team
      </h2>
      <Select
        items={items}
        value={current}
        onValueChange={(v) => void choose(v)}
        disabled={!mayEdit || setTeam.isPending}
      >
        <SelectTrigger
          className="w-full max-w-[420px]"
          aria-label="Responsible team"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {items.map((i) => (
            <SelectItem key={i.value} value={i.value}>
              {i.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-[12.5px] leading-normal text-muted-foreground">
        The agent sends this project&apos;s requests and notices to this team.
      </p>
    </section>
  )
}
