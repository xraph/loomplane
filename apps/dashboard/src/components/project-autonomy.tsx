import { BotIcon } from "lucide-react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useProjectAutonomyUpdate } from "@/client/forge/loomplane/hooks"
import type { Project } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useAgent } from "@/lib/features"
import { useProjectCan } from "@/lib/project-permissions"

// How long a release sits in staging before the agent may promote it alone.
const soaks = [
  { label: "None", value: "0" },
  { label: "30 minutes", value: "1800" },
  { label: "1 hour", value: "3600" },
  { label: "2 hours", value: "7200" },
  { label: "6 hours", value: "21600" },
  { label: "12 hours", value: "43200" },
  { label: "24 hours", value: "86400" },
]

/** soakLabel names a soak in seconds, such as 5400 as "90 minutes". */
function soakLabel(seconds: number): string {
  if (seconds % 3600 === 0) {
    const h = seconds / 3600
    return `${h} ${h === 1 ? "hour" : "hours"}`
  }
  return `${Math.round(seconds / 60)} minutes`
}

/**
 * ProjectAutonomy is whether the agent may promote this project's release
 * without asking first. New projects ask first. It shows only while the
 * agent is on, and only someone who may approve deployments changes it.
 */
export function ProjectAutonomy({ project }: { project: Project }) {
  const agent = useAgent()
  const can = useProjectCan(project.id)
  const save = useMutation(useProjectAutonomyUpdate)
  if (!agent.enabled) return null

  const mayEdit = can("deployment:approve")
  const soak = project.minStagingSoakSeconds ?? 0
  const current = String(soak)
  const items = soaks.some((s) => s.value === current)
    ? soaks
    : [...soaks, { label: soakLabel(soak), value: current }]

  const put = async (
    body: { autonomousPromotion: boolean } | { minStagingSoakSeconds: number },
    done: string
  ) => {
    try {
      await save.mutateAsync({ path: { id: project.id }, body })
      toast.success(done)
    } catch (err) {
      toast.error(messageOf(err, "The change wasn't saved."))
    }
  }

  const control = (
    <Switch
      aria-label="Autonomous promotion"
      checked={!!project.autonomousPromotion}
      disabled={!mayEdit || save.isPending}
      onCheckedChange={(v) =>
        void put(
          { autonomousPromotion: v },
          v
            ? "The agent can now promote on its own"
            : "The agent asks before it promotes"
        )
      }
    />
  )

  return (
    <section
      aria-labelledby="project-autonomy"
      className="grid gap-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-border"
    >
      <h2
        id="project-autonomy"
        className="flex items-center gap-2 text-[14px] font-semibold"
      >
        <BotIcon className="size-4 text-muted-foreground" />
        Autonomy
      </h2>
      <div className="flex items-start justify-between gap-4">
        <div className="grid gap-0.5">
          <span className="text-[13px] font-medium">Autonomous promotion</span>
          <p className="max-w-[56ch] text-[12.5px] leading-normal text-muted-foreground">
            When someone asks the agent to release and every gate is green, it
            promotes without asking first.
          </p>
        </div>
        {mayEdit ? (
          control
        ) : (
          <Tooltip>
            <TooltipTrigger render={<span className="inline-flex" />}>
              {control}
            </TooltipTrigger>
            <TooltipContent side="left">
              Only people who can approve deployments change this
            </TooltipContent>
          </Tooltip>
        )}
      </div>
      <div className="grid gap-1.5">
        <span id="project-soak" className="text-[13px] font-medium">
          Minimum staging soak
        </span>
        <Select
          items={items}
          value={current}
          onValueChange={(v) =>
            v &&
            v !== current &&
            void put(
              { minStagingSoakSeconds: Number(v) },
              "The soak time was saved"
            )
          }
          disabled={!mayEdit || save.isPending}
        >
          <SelectTrigger
            className="w-full max-w-[220px]"
            aria-labelledby="project-soak"
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
          How long a release runs in staging before the agent may promote it
          alone.
        </p>
      </div>
    </section>
  )
}
