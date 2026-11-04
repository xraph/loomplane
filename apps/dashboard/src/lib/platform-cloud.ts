import type {
  PlatformLimits,
  PlatformcloudEvidence,
  PlatformcloudUse,
} from "@/client/forge/loomplane/types"

export type SuspendReason = "spending_limit" | "past_due" | "platform_owner"

export type UsageRow = {
  label: string
  used: number
  limit: number
  text: string
  over: boolean
}

const gib = (mib: number) => Math.round(mib / 1024)

export function usageRows(
  limits: PlatformLimits,
  use: PlatformcloudUse
): UsageRow[] {
  return [
    {
      label: "vCPUs",
      used: use.vcpus,
      limit: limits.vcpus,
      text: `${use.vcpus} of ${limits.vcpus} vCPUs`,
    },
    {
      label: "Memory",
      used: use.memoryMib,
      limit: limits.memoryMib,
      text: `${gib(use.memoryMib)} of ${gib(limits.memoryMib)} GiB memory`,
    },
    {
      label: "VMs",
      used: use.vms,
      limit: limits.vms,
      text: `${use.vms} of ${limits.vms} VMs`,
    },
    {
      label: "Builds",
      used: use.concurrentBuilds,
      limit: limits.concurrentBuilds,
      text: `${use.concurrentBuilds} of ${limits.concurrentBuilds} builds at once`,
    },
  ].map((r) => ({ ...r, over: r.used > r.limit }))
}

const phrases: Record<SuspendReason, string> = {
  spending_limit: "the spending limit was reached",
  past_due: "a payment is past due",
  platform_owner: "the instance owner stopped it",
}

export function suspendMessage(reasons: SuspendReason[]): string | null {
  if (reasons.length === 0) return null
  if (reasons.length === 1 && reasons[0] === "platform_owner") {
    return "Loomplane Cloud work is stopped by the instance owner. It starts again when they restore it."
  }
  const parts = reasons.map((r) => phrases[r])
  const joined =
    parts.length === 1
      ? parts[0]
      : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`
  const when =
    parts.length === 1 ? "it is" : parts.length === 2 ? "both are" : "all are"
  return `Loomplane Cloud work is stopped because ${joined}. It starts again when ${when} resolved.`
}

export function hostEvidenceIssues(
  e: PlatformcloudEvidence,
  now: Date,
  maxAgeMs: number
): string[] {
  const issues: string[] = []
  if (!e.isolation) issues.push("Isolation checks failed")
  if (!e.platform) issues.push("Platform mode is off")
  if (e.smtActive) issues.push("SMT is on")
  const denied = e.deniedPorts ?? []
  if (![25, 465, 587].every((p) => denied.includes(p))) {
    issues.push("Mail ports aren't all closed")
  }
  if (e.perOrgVms <= 0 || e.perOrgVcpus <= 0 || e.perOrgMemoryMib <= 0) {
    issues.push("No per-workspace limit on the host")
  }
  const checked = Date.parse(e.checkedAt)
  if (Number.isNaN(checked) || now.getTime() - checked > maxAgeMs) {
    issues.push(
      `Last check is older than ${Math.floor(maxAgeMs / 60_000)} minutes`
    )
  }
  if (e.reason) issues.push(e.reason)
  return issues
}
