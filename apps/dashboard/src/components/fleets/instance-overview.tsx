import { instanceStatus } from "./instances"
export type InstanceDetails = {
  id: string
  externalRef: string
  status: string
  environmentId: string
  profileId: string
  stageId: string
  revision: number
  desiredGeneration: number
  observedGeneration: number
  desiredReleaseId: string
  observedReleaseId?: string
  updatedAt: string
}
export function InstanceOverview({ instance }: { instance: InstanceDetails }) {
  const facts = [
    ["State", instanceStatus(instance.status)],
    ["Profile", instance.profileId],
    ["Stage", instance.stageId],
    ["Environment", instance.environmentId],
    ["Revision", instance.revision],
    [
      "Desired / observed generation",
      `${instance.desiredGeneration} / ${instance.observedGeneration || "Unknown"}`,
    ],
    ["Desired release", instance.desiredReleaseId],
    ["Observed release", instance.observedReleaseId || "Unknown"],
  ]
  return (
    <section aria-label="Instance overview" className="grid min-w-0 gap-3">
      {instance.status === "waiting_approval" && (
        <p role="status" className="rounded-lg border px-3 py-2 text-sm">
          This request is waiting for policy review. Its desired state has not
          been applied.
        </p>
      )}
      <dl className="grid min-w-0 grid-cols-2 gap-x-6 gap-y-3 lg:grid-cols-4">
        {facts.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd
              aria-label={String(label)}
              className="text-sm font-medium break-words"
            >
              {value}
            </dd>
          </div>
        ))}
      </dl>
      <p className="text-xs text-muted-foreground">
        Record updated{" "}
        <time dateTime={instance.updatedAt}>
          {new Date(instance.updatedAt).toLocaleString()}
        </time>
        . Runtime observations have their own timestamps.
      </p>
    </section>
  )
}
