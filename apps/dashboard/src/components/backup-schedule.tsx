import { useState } from "react"
import type { Policy } from "@/client/forge/loomplane/types"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"

export function BackupSchedule({
  policy,
  disabled,
  save,
  compact = false,
}: {
  compact?: boolean
  policy?: Policy
  disabled: boolean
  save: (hours: number, enabled: boolean) => void
}) {
  const [open, setOpen] = useState(false)
  const [hours, setHours] = useState(String(policy?.everyHours ?? 24))
  const cadence = Number(hours)
  const valid = Number.isInteger(cadence) && cadence >= 1 && cadence <= 168
  const changed = cadence !== (policy?.everyHours ?? 24)
  const form = (
    <form
      className="flex flex-wrap items-end gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        if (valid && !disabled)
          save(cadence, changed ? (policy?.enabled ?? true) : !policy?.enabled)
      }}
    >
      <label className="grid gap-1 text-xs text-muted-foreground">
        Backup interval (hours)
        <Input
          type="number"
          className="h-8 w-24"
          min={1}
          max={168}
          step={1}
          value={hours}
          disabled={disabled}
          onChange={(event) => setHours(event.target.value)}
        />
      </label>
      <Button
        type="submit"
        size="sm"
        variant="outline"
        disabled={disabled || !valid}
      >
        {changed
          ? "Save backup schedule"
          : policy?.enabled
            ? "Pause backup schedule"
            : "Enable backups"}
      </Button>
    </form>
  )
  if (!compact) return form
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        className="h-8 rounded-[9px] px-3 text-[12.5px]"
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        Schedule
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Backup schedule</DialogTitle>
          </DialogHeader>
          {form}
        </DialogContent>
      </Dialog>
    </>
  )
}
