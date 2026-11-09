// What a promotion or a rollback plan adds to the plan tab: where a
// promotion comes from and how long that environment has run it, why a
// rollback was asked for, which images are reused or built again, and
// what a rollback leaves in place.
import { DatabaseIcon } from "lucide-react"

import { usePlansSoak } from "@/client/forge/loomplane/hooks"
import type {
  ArtifactChoice,
  Leftover,
  Plan,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { toneClass } from "@/components/runs"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { duration } from "@/lib/format"
import { Link } from "@/lib/router"
import { usePeople } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** artifactLine says whether a service reuses an image or builds again. */
export function artifactLine(a: ArtifactChoice): string {
  if (a.reused && !a.reference && a.revision)
    return `Returns to its earlier deployment ${a.revision}`
  if (a.reused && a.from) return `Reuses ${a.from}'s image`
  if (a.reused) return "Reuses an earlier image"
  if (a.reason) return `Builds again: ${a.reason}`
  return "Builds again"
}

// What the release a rollback undoes did to a resource it leaves alone.
const leftoverBy: Record<string, string> = {
  delete: "Created by the release",
  create: "Removed by the release",
  update: "Changed by the release",
  replace: "Replaced by the release",
}

/** soakLine says how long the source has run the commits, as loomctl does. */
function SoakLine({ plan }: { plan: Plan }) {
  const soak = useQuery(usePlansSoak, { path: { id: plan.id } })
  if (soak.status === "error")
    return (
      <p className="text-[12.5px] text-muted-foreground">
        How long {plan.fromEnvironment} has run these commits couldn&apos;t be
        read.
      </p>
    )
  const s = soak.data
  if (!s) return null
  const reasons = s.reasons.join("; ")
  const took = duration(Math.round(s.duration / 1e6 / 60_000) * 60_000)
  const tone = !s.running ? "quiet" : s.healthy ? "live" : "failed"
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span
        aria-hidden="true"
        className={cn("size-1.5 rounded-full", toneClass[tone].dot)}
      />
      <p className="text-[13px]">
        {!s.running
          ? `${s.environment} doesn't run these commits yet${reasons ? `: ${reasons}` : ""}`
          : s.healthy
            ? `${s.environment} has run these commits for ${took}, healthy`
            : `${s.environment} has run these commits for ${took}, not healthy${reasons ? `: ${reasons}` : ""}`}
      </p>
      {s.unverified && (
        <Badge
          variant="secondary"
          className={cn(toneClass.waiting.tint, toneClass.waiting.ink)}
        >
          Unverified
        </Badge>
      )}
    </div>
  )
}

/** ReleaseKind is the promotion's source or the rollback's reason. */
export function ReleaseKind({ plan }: { plan: Plan }) {
  const person = usePeople()
  if (plan.kind === "promotion") {
    return (
      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="text-sm">Source environment</CardTitle>
          <CardDescription className="text-xs">
            {plan.environment} moves to exactly what {plan.fromEnvironment}{" "}
            runs.
          </CardDescription>
        </CardHeader>
        {plan.status !== "planning" && (
          <CardContent>
            <SoakLine plan={plan} />
          </CardContent>
        )}
      </Card>
    )
  }
  const r = plan.rollback
  if (plan.kind !== "rollback" || !r) return null
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="text-sm">Rollback</CardTitle>
        <CardDescription className="text-xs">
          {person(plan.createdBy).name} asked for it. It restores the release
          from{" "}
          <Link
            to="/deployments/$id"
            params={{ id: r.restorePlanId }}
            className="underline underline-offset-4"
          >
            {r.restoreDeploymentId}
          </Link>{" "}
          and undoes{" "}
          <Link
            to="/deployments/$id"
            params={{ id: r.targetPlanId }}
            className="underline underline-offset-4"
          >
            {r.targetDeploymentId}
          </Link>
          .
        </CardDescription>
      </CardHeader>
      {r.reason && (
        <CardContent>
          <p className="text-[13px] whitespace-pre-wrap">{r.reason}</p>
        </CardContent>
      )}
    </Card>
  )
}

/** Artifacts lists each service's image: reused, or built again and why. */
export function Artifacts({ artifacts }: { artifacts: ArtifactChoice[] }) {
  if (artifacts.length === 0) return null
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="text-sm">Artifacts</CardTitle>
        <CardDescription className="text-xs">
          A reused image is the one already tested, so it isn&apos;t built
          again.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ul aria-label="Artifacts" className="grid gap-2">
          {artifacts.map((a) => (
            <li key={a.service} className="flex flex-wrap gap-x-2 text-[13px]">
              <span className="font-mono font-medium">{a.service}</span>
              <span
                className={cn(
                  a.reused ? "text-muted-foreground" : "text-weld-ink"
                )}
              >
                {artifactLine(a)}
              </span>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

/** Leftovers lists what a rollback leaves as the release it undoes made it. */
export function Leftovers({ leftovers }: { leftovers: Leftover[] }) {
  if (leftovers.length === 0) return null
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="text-sm">Leftovers</CardTitle>
        <CardDescription className="text-xs">
          The rollback leaves these as the release it undoes made them. It never
          deletes, replaces or reverts anything that holds data, so clean them
          up yourself once you&apos;re sure.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ul aria-label="Leftovers" className="grid gap-3">
          {leftovers.map((l) => (
            <li key={`${l.subject}:${l.name}`} className="grid gap-0.5">
              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <span className="font-mono font-medium">{l.name}</span>
                <span className="text-muted-foreground">{l.subject}</span>
                {l.dataBearing && (
                  <Badge
                    variant="secondary"
                    className={cn(
                      "gap-1",
                      toneClass.waiting.tint,
                      toneClass.waiting.ink
                    )}
                  >
                    <DatabaseIcon />
                    Data
                  </Badge>
                )}
              </div>
              <p className="text-[12.5px] text-muted-foreground">
                {leftoverBy[l.op] ?? "Left by the release"}
                {l.reason ? `. ${l.reason}` : ""}
              </p>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}
