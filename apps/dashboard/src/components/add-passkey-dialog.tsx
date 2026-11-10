// Adding a passkey from the account's Security tab. The browser shows its
// own prompt (Touch ID, Windows Hello, a security key); this dialog names
// the passkey first and, with two-step on, takes a code, since a passkey
// that checks your fingerprint or PIN signs in without one.
import { PlusIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { CodeField } from "@/components/two-step"
import { FieldProblem } from "@/components/settings-section"
import { Button } from "@/components/ui/button"
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
import {
  addPasskey,
  describeAgent,
  factorProblem,
  useAccountClient,
} from "@/lib/account"

export function AddPasskeyDialog({
  twoStep,
  onAdded,
}: {
  twoStep: boolean
  onAdded: () => void
}) {
  const { client, token } = useAccountClient()
  const nameId = useId()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [code, setCode] = useState("")
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    setBusy(true)
    try {
      await addPasskey(client, token, name.trim(), twoStep ? code : undefined)
      setOpen(false)
      onAdded()
    } catch (err) {
      // Closing the browser's prompt is a choice, not a failure.
      if (err instanceof DOMException && err.name === "NotAllowedError") {
        setProblem("No passkey was added. Try again when you're ready.")
      } else {
        setProblem(factorProblem(err, "The passkey wasn't added."))
      }
    } finally {
      setBusy(false)
      setCode("")
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setName(describeAgent(navigator.userAgent).label)
        setCode("")
        setProblem("")
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <PlusIcon />
        Add a passkey
      </DialogTrigger>
      <DialogContent className="sm:max-w-[440px]">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Add a passkey</DialogTitle>
            <DialogDescription>
              Your browser asks for your fingerprint, face, PIN or security key
              next.
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor={nameId}>Name</FieldLabel>
            <Input
              id={nameId}
              value={name}
              maxLength={64}
              onChange={(e) => setName(e.target.value)}
              required
            />
            <FieldDescription>
              So you can tell your passkeys apart later.
            </FieldDescription>
          </Field>
          {twoStep && (
            <CodeField
              label="Code from the app or a recovery code"
              value={code}
              onChange={setCode}
              recovery
            />
          )}
          <FieldProblem text={problem} />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              Continue
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
