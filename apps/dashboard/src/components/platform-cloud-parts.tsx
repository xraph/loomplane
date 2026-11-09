import type { ReactNode } from "react"

import { messageOf } from "@/client/errors"
import { PlatformCloudIcon } from "@/components/icons"
import { StatusPill, type Tone } from "@/components/runs"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { statusLabel } from "@/lib/platform-cloud"
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
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

/** evidenceMaxAgeMs is how old a host's last check may be before it is called out. */
export const evidenceMaxAgeMs = 10 * 60_000

export const gibOf = (mib: number) => Math.round(mib / 1024)

/** countOf reads a number input: a whole number, or null for blanks and junk. */
export function countOf(v: string): number | null {
  const t = v.trim()
  return /^\d+$/.test(t) ? Number(t) : null
}

/**
 * mibOf reads a GiB input as MiB. A field the owner left as shown keeps the
 * stored MiB, so rounding for display never changes a saved value.
 */
export function mibOf(
  v: string,
  was?: { shown: string; mib: number }
): number | null {
  if (was && v === was.shown) return was.mib
  const n = countOf(v)
  return n === null ? null : n * 1024
}

/** errorOf is messageOf without the server's "platformcloud: " prefix. */
export function errorOf(err: unknown, fallback: string): string {
  const m = messageOf(err, fallback).replace(/^platformcloud:\s*/i, "")
  return m.charAt(0).toUpperCase() + m.slice(1)
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
  const invalid = countOf(value) === null
  return (
    <Field data-invalid={invalid || undefined}>
      <FieldLabel>{label}</FieldLabel>
      <Input
        aria-label={label}
        aria-invalid={invalid || undefined}
        inputMode="numeric"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      {invalid && <FieldError>Enter a whole number</FieldError>}
    </Field>
  )
}

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

/** PlatformCloudEmpty keeps section empty states on the shared inline treatment. */
export function PlatformCloudEmpty({
  title,
  children,
  action,
}: {
  title: string
  children: ReactNode
  action?: ReactNode
}) {
  return (
    <ZeroState variant="inline" preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <PlatformCloudIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
      {action && <ZeroStateContent>{action}</ZeroStateContent>}
    </ZeroState>
  )
}

const platformTones: Record<string, Tone> = {
  ready: "live",
  active: "live",
  enabled: "live",
  running: "live",
  reserved: "flight",
  pending: "waiting",
  draining: "waiting",
  suspended: "waiting",
  failed: "failed",
  error: "failed",
  blocked: "failed",
}

/** PlatformCloudStatus keeps domain labels while using shared semantic tones. */
export function PlatformCloudStatus({
  kind,
  status,
  waitingCapacity = false,
}: {
  kind: Parameters<typeof statusLabel>[0]
  status: string
  waitingCapacity?: boolean
}) {
  return (
    <StatusPill
      tone={waitingCapacity ? "waiting" : (platformTones[status] ?? "quiet")}
    >
      {waitingCapacity ? "Waiting for room" : statusLabel(kind, status)}
    </StatusPill>
  )
}
