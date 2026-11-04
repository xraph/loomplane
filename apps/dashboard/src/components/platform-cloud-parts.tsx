import type { ReactNode } from "react"

import type {
  PlatformLimits,
  PlatformcloudEvidence,
} from "@/client/forge/loomplane/types"
import {
  type PlatformCloudAmounts,
  type PlatformHostEvidence,
  type SuspendReason,
} from "@/lib/platform-cloud"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

/** evidenceMaxAgeMs is how old a host's last check may be before it is called out. */
export const evidenceMaxAgeMs = 10 * 60_000

export const gibOf = (mib: number) => Math.round(mib / 1024)

/** amountsOf maps generated limits or use to the helper's wire-shaped amounts. */
export function amountsOf(a: PlatformLimits): PlatformCloudAmounts {
  return {
    vcpus: a.vcpus,
    memory_mib: a.memoryMib,
    vms: a.vms,
    concurrent_builds: a.concurrentBuilds,
  }
}

export function evidenceOf(e: PlatformcloudEvidence): PlatformHostEvidence {
  return {
    isolation: e.isolation,
    platform: e.platform,
    smt_active: e.smtActive,
    denied_ports: e.deniedPorts,
    per_org_vms: e.perOrgVms,
    per_org_vcpus: e.perOrgVcpus,
    per_org_memory_mib: e.perOrgMemoryMib,
    checked_at: e.checkedAt,
    reason: e.reason,
  }
}

export const reasonsOf = (r: string[]) => r as SuspendReason[]

/** toCount reads a number input, treating blanks and junk as 0. */
export function toCount(v: string): number {
  const n = Math.floor(Number(v))
  return Number.isFinite(n) && n > 0 ? n : 0
}

export function NumberField({
  label,
  value,
  onChange,
}: {
  label: string
  value: string
  onChange: (v: string) => void
}) {
  return (
    <Field>
      <FieldLabel>{label}</FieldLabel>
      <Input
        aria-label={label}
        inputMode="numeric"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  )
}

export const selectClass =
  "h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm"

export function Confirm({
  title,
  description,
  action,
  onConfirm,
  onClose,
}: {
  title: string
  description: ReactNode
  action: string
  onConfirm: () => void
  onClose: () => void
}) {
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>{action}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
