import type { ReactNode } from "react"

import { messageOf } from "@/client/errors"
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
