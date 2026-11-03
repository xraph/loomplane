// Changing your password, from the account's Security tab. It checks what
// it can before asking authsome: the two new passwords match and are long
// enough. Signing out the other sessions is on by default, since a stolen
// password is the usual reason to change one.
import { CheckIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf } from "@/client/errors"
import { FieldProblem } from "@/components/settings-section"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { useAccountClient } from "@/lib/account"
import { cn } from "@/lib/utils"

const minimum = 8

/** strength scores a new password 0 (empty) to 4, on length alone. */
function strength(password: string): number {
  const n = password.length
  return n === 0 ? 0 : n < minimum ? 1 : n < 12 ? 2 : n < 16 ? 3 : 4
}

const strengthNote = [
  `At least ${minimum} characters. A short sentence is easier to remember.`,
  `Too short. Use at least ${minimum} characters.`,
  "Fine. A few more characters makes it much harder to guess.",
  "Good.",
  "Strong.",
]

export function ChangePasswordDialog({ onChanged }: { onChanged: () => void }) {
  const { client, token } = useAccountClient()
  const ids = { current: useId(), next: useId(), again: useId(), out: useId() }
  const [open, setOpen] = useState(false)
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [again, setAgain] = useState("")
  const [others, setOthers] = useState(true)
  const [saving, setSaving] = useState(false)
  const [done, setDone] = useState(false)
  const [problem, setProblem] = useState("")

  const reset = () => {
    setCurrent("")
    setNext("")
    setAgain("")
    setOthers(true)
    setSaving(false)
    setDone(false)
    setProblem("")
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    if (next.length < minimum) {
      setProblem(`The new password needs at least ${minimum} characters.`)
      return
    }
    if (next !== again) {
      setProblem("The two new passwords don't match.")
      return
    }
    setSaving(true)
    try {
      await client.changePassword(
        { current_password: current, new_password: next },
        token
      )
      if (others) await client.revokeOtherSessions(token)
      setDone(true)
      onChanged()
    } catch (err) {
      setProblem(messageOf(err, "Your password wasn't changed."))
    } finally {
      setSaving(false)
      // Never left in the page, changed or not.
      setCurrent("")
      setNext("")
      setAgain("")
    }
  }

  const score = strength(next)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) reset()
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        Change password
      </DialogTrigger>
      <DialogContent className="sm:max-w-[460px]">
        {done ? (
          <>
            <DialogHeader>
              <span
                aria-hidden="true"
                className="mb-1 grid size-10 place-items-center rounded-full bg-overdye-tint text-overdye-ink"
              >
                <CheckIcon className="size-5" />
              </span>
              <DialogTitle>Password changed</DialogTitle>
              <DialogDescription>
                {others
                  ? "Your other sessions are signed out. You're still signed in here."
                  : "You're still signed in everywhere you were."}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button onClick={() => setOpen(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={(e) => void submit(e)} className="grid gap-4">
            <DialogHeader>
              <DialogTitle>Change password</DialogTitle>
              <DialogDescription>
                You&apos;ll stay signed in here.
              </DialogDescription>
            </DialogHeader>
            <Field>
              <FieldLabel htmlFor={ids.current}>Current password</FieldLabel>
              <Input
                id={ids.current}
                type="password"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={ids.next}>New password</FieldLabel>
              <Input
                id={ids.next}
                type="password"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
              <div aria-hidden="true" className="grid grid-cols-4 gap-1">
                {[1, 2, 3, 4].map((n) => (
                  <span
                    key={n}
                    className={cn(
                      "h-1 rounded-full",
                      n > score
                        ? "bg-muted"
                        : score === 1
                          ? "bg-destructive"
                          : score === 2
                            ? "bg-weld"
                            : score === 3
                              ? "bg-flight"
                              : "bg-overdye"
                    )}
                  />
                ))}
              </div>
              <FieldDescription>{strengthNote[score]}</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor={ids.again}>Type it again</FieldLabel>
              <Input
                id={ids.again}
                type="password"
                autoComplete="new-password"
                value={again}
                onChange={(e) => setAgain(e.target.value)}
                required
              />
            </Field>
            <label
              htmlFor={ids.out}
              className="flex cursor-pointer items-start gap-2.5 rounded-[10px] bg-subtle px-3 py-2.5 text-[13px] ring-1 ring-border"
            >
              <Checkbox
                id={ids.out}
                checked={others}
                onCheckedChange={(v) => setOthers(v === true)}
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="font-medium">Sign out everywhere else</span>
                <span className="text-[12px] text-muted-foreground">
                  Other browsers and the mobile app ask for the new password.
                </span>
              </span>
            </label>
            <FieldProblem text={problem} />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={saving}>
                {saving && <Spinner />}
                Change password
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
