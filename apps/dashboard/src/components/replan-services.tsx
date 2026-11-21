import { useState } from "react"
import type { Plan } from "@/client/forge/loomplane/types"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { planServices } from "@/lib/plans"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Checkbox } from "@/components/ui/checkbox"

export function ReplanServices({
  plan,
  planAgain,
  pending,
}: {
  plan: Plan
  planAgain: (services: string[]) => Promise<void>
  pending: boolean
}) {
  const [excluded, setExcluded] = useState<string[]>([])
  const scoped = planServices(plan)
  // A service can fail validation before the planner creates its action.
  const names = scoped.length
    ? scoped
    : [
        ...new Set([
          ...plan.actions
            .filter(
              (a) =>
                a.subject === "service" &&
                a.op !== "delete" &&
                !a.service?.gateway &&
                !a.service?.databaseGateway &&
                !plan.sources.some(
                  (s) => !s.inScope && s.alias === a.service?.repo
                )
            )
            .map((a) => a.name),
          ...plan.problems
            .filter(
              (f) =>
                f.name &&
                (f.path?.startsWith(`services.${f.name}.`) ||
                  f.path === `services.${f.name}`)
            )
            .map((f) => f.name!),
        ]),
      ]
  if (names.length < 2) return null
  const remaining = names.filter((name) => !excluded.includes(name))
  return (
    <Dialog
      onOpenChange={(open) => {
        if (open) setExcluded([])
      }}
    >
      <DialogTrigger render={<Button size="sm" variant="outline" />}>
        Choose services
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Choose services for the next plan</DialogTitle>
          <DialogDescription>
            Uncheck a service to leave it out. If it already runs in{" "}
            {plan.environment}, it keeps running.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-1">
          {names.map((name) => (
            <label key={name} className="flex min-h-9 items-center gap-2">
              <Checkbox
                aria-label={name}
                checked={!excluded.includes(name)}
                disabled={pending}
                onCheckedChange={(checked) =>
                  setExcluded((previous) =>
                    checked
                      ? previous.filter((n) => n !== name)
                      : [...previous, name]
                  )
                }
              />
              <span className="min-w-0 break-all">{name}</span>
            </label>
          ))}
        </div>
        <DialogFooter>
          <Button
            disabled={
              pending || !remaining.length || remaining.length === names.length
            }
            onClick={() => {
              if (remaining.length) void planAgain(remaining)
            }}
          >
            {pending && <Spinner />}Plan with {remaining.length}{" "}
            {remaining.length === 1 ? "service" : "services"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
