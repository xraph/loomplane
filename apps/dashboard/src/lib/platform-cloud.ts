export type PlatformCloudAmounts = {
  vcpus: number
  memory_mib: number
  vms: number
  concurrent_builds: number
}

export type SuspendReason = "spending_limit" | "past_due" | "platform_owner"

export type PlatformHostEvidence = {
  isolation: boolean
  platform: boolean
  smt_active: boolean
  denied_ports: number[] | null
  per_org_vms: number
  per_org_vcpus: number
  per_org_memory_mib: number
  checked_at: string
  reason?: string
}

export type UsageRow = {
  label: string
  used: number
  limit: number
  text: string
  over: boolean
}

const gib = (mib: number) => Math.round(mib / 1024)

export function usageRows(limits: PlatformCloudAmounts, use: PlatformCloudAmounts): UsageRow[] {
  return [
    {
      label: "vCPUs",
      used: use.vcpus,
      limit: limits.vcpus,
      text: `${use.vcpus} of ${limits.vcpus} vCPUs`,
    },
    {
      label: "Memory",
      used: use.memory_mib,
      limit: limits.memory_mib,
      text: `${gib(use.memory_mib)} of ${gib(limits.memory_mib)} GiB memory`,
    },
    {
      label: "VMs",
      used: use.vms,
      limit: limits.vms,
      text: `${use.vms} of ${limits.vms} VMs`,
    },
    {
      label: "Builds",
      used: use.concurrent_builds,
      limit: limits.concurrent_builds,
      text: `${use.concurrent_builds} of ${limits.concurrent_builds} builds at once`,
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
  const when = parts.length === 1 ? "it is" : parts.length === 2 ? "both are" : "all are"
  return `Loomplane Cloud work is stopped because ${joined}. It starts again when ${when} resolved.`
}

export function hostEvidenceIssues(e: PlatformHostEvidence, now: Date, maxAgeMs: number): string[] {
  const issues: string[] = []
  if (!e.isolation) issues.push("Isolation checks failed")
  if (!e.platform) issues.push("Platform mode is off")
  if (e.smt_active) issues.push("SMT is on")
  const denied = e.denied_ports ?? []
  if (![25, 465, 587].every((p) => denied.includes(p))) {
    issues.push("Mail ports aren't all closed")
  }
  if (e.per_org_vms <= 0 || e.per_org_vcpus <= 0 || e.per_org_memory_mib <= 0) {
    issues.push("No per-workspace limit on the host")
  }
  const checked = Date.parse(e.checked_at)
  if (Number.isNaN(checked) || now.getTime() - checked > maxAgeMs) {
    issues.push(`Last check is older than ${Math.floor(maxAgeMs / 60_000)} minutes`)
  }
  if (e.reason) issues.push(e.reason)
  return issues
}
