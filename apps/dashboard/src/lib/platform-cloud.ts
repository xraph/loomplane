import type {
  CertificateView,
  HostView,
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

/** isSuspendReason tells the three reasons this page knows from anything else. */
export const isSuspendReason = (r: string): r is SuspendReason =>
  Object.hasOwn(phrases, r)

/**
 * suspendMessage explains why a workspace's Loomplane Cloud work is stopped.
 * A reason it doesn't know is named only as something else needing attention.
 */
export function suspendMessage(reasons: readonly string[]): string | null {
  if (reasons.length === 0) return null
  const known = reasons.filter(isSuspendReason)
  const other = known.length < reasons.length
  if (!other && known.length === 1 && known[0] === "platform_owner") {
    return "Loomplane Cloud work is stopped by the instance owner. It starts again when they restore it."
  }
  const parts = [...new Set(known)].map((r) => phrases[r])
  if (other) parts.push("something else needs attention")
  const joined =
    parts.length === 1
      ? parts[0]
      : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`
  const when =
    parts.length === 1 ? "it is" : parts.length === 2 ? "both are" : "all are"
  return `Loomplane Cloud work is stopped because ${joined}. It starts again when ${when} resolved.`
}

/** neverChecked is true when a host's evidence was never gathered. */
function neverChecked(e: PlatformcloudEvidence): boolean {
  // Go's zero time parses to a date before 1970.
  const at = e.checkedAt ? Date.parse(e.checkedAt) : Number.NaN
  const zero = !e.checkedAt || (!Number.isNaN(at) && at <= 0)
  return zero || !e.protocolVersion || e.protocolVersion === "0"
}

export function hostEvidenceIssues(
  e: PlatformcloudEvidence,
  now: Date,
  maxAgeMs: number
): string[] {
  if (neverChecked(e)) {
    return e.reason ? ["Not checked yet", e.reason] : ["Not checked yet"]
  }
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

type StatusKind = "host" | "region" | "tenant" | "placement"

const statusWords: Record<StatusKind, Record<string, string>> = {
  host: {
    pending: "Not ready",
    ready: "Ready",
    draining: "Draining",
    retired: "Retired",
  },
  region: { active: "Open", closed: "Closed" },
  tenant: { enabled: "Enabled", disabled: "Disabled" },
  placement: {
    reserved: "Starting",
    running: "Running",
    stopped: "Stopped",
    suspended: "Suspended",
    released: "Removed",
  },
}

/** statusLabel turns a stored status into sentence-case words. */
export function statusLabel(kind: StatusKind, status: string): string {
  const known = statusWords[kind][status]
  if (known) return known
  if (!status) return "Unknown"
  const words = status.replace(/[_-]+/g, " ")
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export type CertificateNote = { text: string; urgent: boolean }

const days = (n: number) => `${n} ${n === 1 ? "day" : "days"}`

/**
 * hostCertificateNotes says how a host's client certificate stands: how
 * soon it runs out, and what a rotation under way is waiting for.
 */
export function hostCertificateNotes(
  c: CertificateView | undefined
): CertificateNote[] {
  if (!c) return []
  const notes: CertificateNote[] = []
  const left = c.daysLeft ?? 0
  switch (c.warning) {
    case "expired":
      notes.push({
        text: "The client certificate has expired. Rotate it now.",
        urgent: true,
      })
      break
    case "urgent":
      notes.push({
        text: `The client certificate expires in ${days(left)}. Rotate it now.`,
        urgent: true,
      })
      break
    case "expiring":
      notes.push({
        text: `The client certificate expires in ${days(left)}`,
        urgent: false,
      })
      break
  }
  if (c.rotation === "waiting") {
    notes.push({
      text: "Rotation: add the new certificate to the host and reload its agent",
      urgent: false,
    })
  } else if (c.rotation === "retiring") {
    notes.push({
      text: "Rotation: remove the old certificate from the host and reload its agent",
      urgent: false,
    })
  }
  return notes
}

/** expiringHosts counts the hosts in service whose certificate needs rotating. */
export function expiringHosts(hosts: readonly HostView[] | undefined): number {
  return (hosts ?? []).filter(
    (h) => h.status !== "retired" && (h.certificate?.warning ?? "") !== ""
  ).length
}
