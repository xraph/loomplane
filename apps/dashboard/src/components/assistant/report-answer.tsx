// Report: the way to tell whoever runs this Loomplane that an agent's
// answer was wrong, offensive or harmful. Google Play asks for it in the
// app, and it's useful anywhere the agent talks to people.
import { useState } from "react"

import { messageOf } from "@/client/errors"
import { useAgentConversationsFlag } from "@/client/forge/loomplane/hooks"
import { useMutation } from "@/client/hooks"
import { FlaggedAnswersIcon as FlagIcon } from "@/components/icons"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Textarea } from "@/components/ui/textarea"
import { FLAG_NOTE_MAX, flagReasons } from "@/lib/agent-flags"

export const REPORT_THANKS =
  "Thanks. The report went to whoever runs this Loomplane."

/** ReportAnswer is the small Report action under a finished answer. */
export function ReportAnswer({
  conversationId,
  taskId,
}: {
  conversationId: string
  taskId: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Report answer"
        title="Report answer"
        className="text-muted-foreground"
        onClick={() => setOpen(true)}
      >
        <FlagIcon />
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          {open && (
            <ReportForm conversationId={conversationId} taskId={taskId} />
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}

function ReportForm({
  conversationId,
  taskId,
}: {
  conversationId: string
  taskId: string
}) {
  const flag = useMutation(useAgentConversationsFlag)
  const [reason, setReason] = useState<string>("inaccurate")
  const [note, setNote] = useState("")
  const [error, setError] = useState<string>()
  const [sent, setSent] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(undefined)
    try {
      await flag.mutateAsync({
        path: { id: conversationId, task: taskId },
        body: { reason, ...(note.trim() ? { note: note.trim() } : {}) },
      })
      setSent(true)
    } catch (err) {
      setError(messageOf(err, "The report couldn't be sent. Try again."))
    }
  }

  if (sent)
    return (
      <DialogHeader>
        <DialogTitle>Report sent</DialogTitle>
        <DialogDescription>{REPORT_THANKS}</DialogDescription>
      </DialogHeader>
    )

  return (
    <form onSubmit={submit} className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Report this answer</DialogTitle>
        <DialogDescription>
          Tell whoever runs this Loomplane what was wrong with it. They see the
          question, this answer and your note, and nothing else from the
          conversation. They keep the report after the conversation is deleted.
        </DialogDescription>
      </DialogHeader>
      <fieldset className="grid gap-2">
        <legend className="mb-1 text-[12.5px] font-medium">Reason</legend>
        {flagReasons.map((r) => (
          <label key={r.value} className="flex items-center gap-2 text-[13px]">
            <input
              type="radio"
              name="reason"
              value={r.value}
              checked={reason === r.value}
              onChange={() => setReason(r.value)}
              className="accent-primary"
            />
            {r.label}
          </label>
        ))}
      </fieldset>
      <label className="grid gap-1.5 text-[12.5px] font-medium">
        Note (optional)
        <Textarea
          value={note}
          maxLength={FLAG_NOTE_MAX}
          onChange={(e) => setNote(e.target.value)}
          rows={3}
          className="font-normal"
        />
      </label>
      {error && (
        <p role="alert" className="text-[13px] text-destructive">
          {error}
        </p>
      )}
      <DialogFooter>
        <Button type="submit" disabled={flag.isPending}>
          Send report
        </Button>
      </DialogFooter>
    </form>
  )
}
