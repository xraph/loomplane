// Two-step verification on the account's Security tab: setting it up with
// an authenticator app, turning it off, and replacing the recovery codes.
// Every call goes through authsome's SDK. Turning it on or off signs out
// your other sessions, which authsome does itself.
import { CheckIcon, CopyIcon, DownloadIcon } from "lucide-react"
import { type FormEvent, useId, useMemo, useState } from "react"
import { encode } from "uqr"

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
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { factorProblem, sixDigits, useAccountClient } from "@/lib/account"

/** QrCode draws value as a QR code, one path of dark modules. */
export function QrCode({ value, label }: { value: string; label: string }) {
  const { size, path } = useMemo(() => {
    const { data } = encode(value, { border: 2 })
    let d = ""
    data.forEach((row, y) =>
      row.forEach((dark, x) => {
        if (dark) d += `M${x} ${y}h1v1h-1z`
      })
    )
    return { size: data.length, path: d }
  }, [value])
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${size} ${size}`}
      shapeRendering="crispEdges"
      className="size-[152px] flex-none rounded-[12px] bg-white ring-1 ring-border"
    >
      <path d={path} fill="#1A1614" />
    </svg>
  )
}

/** CodeField is the one input every two-step form asks for. */
export function CodeField({
  label,
  value,
  onChange,
  recovery = false,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  recovery?: boolean
}) {
  const id = useId()
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        // A recovery code has letters and a dash; an app's code is digits.
        inputMode={recovery ? "text" : "numeric"}
        autoComplete="one-time-code"
        maxLength={recovery ? 32 : 6}
        placeholder="123456"
        value={value}
        onChange={(e) => onChange(e.target.value.trim())}
        className="max-w-[180px] font-mono text-[16px] tracking-[0.2em]"
        required
      />
    </Field>
  )
}

/**
 * RecoveryCodes shows codes that are never shown again, with ways to keep
 * them, and the checkbox that has to be ticked before the dialog finishes.
 */
function RecoveryCodes({
  codes,
  kept,
  onKept,
}: {
  codes: string[]
  kept: boolean
  onKept: (v: boolean) => void
}) {
  const id = useId()
  const [copied, setCopied] = useState(false)
  const text = codes.join("\n")
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
    } catch {
      // Clipboard refused; the codes are still on screen to copy by hand.
    }
  }
  const download = () => {
    const url = URL.createObjectURL(
      new Blob([`Loomplane recovery codes\n\n${text}\n`], {
        type: "text/plain",
      })
    )
    const a = document.createElement("a")
    a.href = url
    a.download = "loomplane-recovery-codes.txt"
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="grid gap-3.5">
      <ul className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-[10px] bg-subtle px-3.5 py-3 font-mono text-[13px]">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => void copy()}>
          {copied ? <CheckIcon /> : <CopyIcon />}
          {copied ? "Copied" : "Copy all"}
        </Button>
        <Button variant="outline" size="sm" onClick={download}>
          <DownloadIcon />
          Download .txt
        </Button>
      </div>
      <label
        htmlFor={id}
        className="flex cursor-pointer items-center gap-2.5 text-[13px]"
      >
        <Checkbox
          id={id}
          checked={kept}
          onCheckedChange={(v) => onKept(v === true)}
        />
        I&apos;ve saved these somewhere safe
      </label>
    </div>
  )
}

const recoveryNote =
  "If you lose your phone, each of these gets you in once. This is the only time you'll see them, so keep them in your password manager."

/**
 * SetUpTwoStep connects an authenticator app in two steps: scan and enter a
 * code, which turns two-step on, then save the recovery codes.
 */
export function SetUpTwoStep({ onChanged }: { onChanged: () => void }) {
  const { client, token } = useAccountClient()
  const [open, setOpen] = useState(false)
  const [secret, setSecret] = useState<{ key: string; url: string }>()
  const [code, setCode] = useState("")
  const [codes, setCodes] = useState<string[]>()
  const [kept, setKept] = useState(false)
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")

  const start = async () => {
    setBusy(true)
    try {
      const res = await client.enrollMFA({ method: "totp" }, token)
      setSecret({ key: res.secret, url: res.otpauth_url })
    } catch (err) {
      setProblem(factorProblem(err, "Two-step verification couldn't start."))
    } finally {
      setBusy(false)
    }
  }

  const change = (o: boolean) => {
    setOpen(o)
    if (o) {
      void start()
      return
    }
    // Verifying turned it on already, whether or not the codes were saved.
    if (codes) onChanged()
    setSecret(undefined)
    setCode("")
    setCodes(undefined)
    setKept(false)
    setProblem("")
  }

  const verify = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    if (!sixDigits(code)) {
      setProblem("Enter the 6-digit code your app shows.")
      return
    }
    setBusy(true)
    try {
      const res = await client.verifyMFA({ code })
      setCodes(res.recovery_codes ?? [])
    } catch (err) {
      setProblem(factorProblem(err, "That code didn't turn it on."))
    } finally {
      setBusy(false)
      setCode("")
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={<Button size="sm" />}>Set up</DialogTrigger>
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <span className="text-[12px] font-medium text-muted-foreground">
            Step {codes ? 2 : 1} of 2
          </span>
          <DialogTitle>
            {codes
              ? "Save your recovery codes"
              : "Connect your authenticator app"}
          </DialogTitle>
          <DialogDescription>
            {codes
              ? recoveryNote
              : "Scan this with your authenticator app, then type the 6-digit code it shows."}
          </DialogDescription>
        </DialogHeader>
        {codes ? (
          <>
            <RecoveryCodes codes={codes} kept={kept} onKept={setKept} />
            <DialogFooter>
              <Button disabled={!kept} onClick={() => change(false)}>
                Turn on two-step verification
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={(e) => void verify(e)} className="grid gap-4">
            {secret ? (
              <div className="flex flex-wrap items-center gap-4">
                <QrCode
                  value={secret.url}
                  label="QR code for your authenticator app"
                />
                <div className="grid min-w-[200px] flex-1 gap-1.5">
                  <span className="text-[12.5px] text-muted-foreground">
                    Can&apos;t scan it? Type this key instead.
                  </span>
                  <code className="rounded-[9px] bg-subtle px-2.5 py-2 font-mono text-[12.5px] tracking-[0.06em] break-all">
                    {secret.key}
                  </code>
                </div>
              </div>
            ) : (
              busy && <Skeleton className="h-[152px] w-full rounded-[12px]" />
            )}
            {secret && (
              <CodeField
                label="Code from the app"
                value={code}
                onChange={setCode}
              />
            )}
            <FieldProblem text={problem} />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => change(false)}
              >
                Cancel
              </Button>
              {secret && (
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner />}
                  Verify
                </Button>
              )}
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** TurnOffTwoStep takes a current code, or a recovery code, to turn it off. */
export function TurnOffTwoStep({ onChanged }: { onChanged: () => void }) {
  const { client, token } = useAccountClient()
  const [open, setOpen] = useState(false)
  const [code, setCode] = useState("")
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    setBusy(true)
    try {
      await client.disableMFA({ code }, token)
      setOpen(false)
      onChanged()
    } catch (err) {
      setProblem(factorProblem(err, "Two-step verification is still on."))
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
        setCode("")
        setProblem("")
      }}
    >
      <DialogTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            className="text-destructive hover:text-destructive"
          />
        }
      >
        Turn off
      </DialogTrigger>
      <DialogContent className="sm:max-w-[440px]">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Turn off two-step verification?</DialogTitle>
            <DialogDescription>
              Your password alone will sign you in again, and your other
              sessions are signed out.
            </DialogDescription>
          </DialogHeader>
          <CodeField
            label="Code from the app or a recovery code"
            value={code}
            onChange={setCode}
            recovery
          />
          <FieldProblem text={problem} />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button type="submit" variant="destructive" disabled={busy}>
              {busy && <Spinner />}
              Turn off two-step
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** NewRecoveryCodes replaces the recovery codes and shows the new ones once. */
export function NewRecoveryCodes() {
  const { client, token } = useAccountClient()
  const [open, setOpen] = useState(false)
  const [codes, setCodes] = useState<string[]>()
  const [kept, setKept] = useState(false)
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")

  const make = async () => {
    setProblem("")
    setBusy(true)
    try {
      setCodes((await client.regenerateMFARecoveryCodes(token)).codes)
    } catch (err) {
      setProblem(factorProblem(err, "No new codes were made."))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) {
          setCodes(undefined)
          setKept(false)
          setProblem("")
        }
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        Make new ones
      </DialogTrigger>
      <DialogContent className="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>
            {codes
              ? "Save your new recovery codes"
              : "Make new recovery codes?"}
          </DialogTitle>
          <DialogDescription>
            {codes ? recoveryNote : "Your old codes stop working, used or not."}
          </DialogDescription>
        </DialogHeader>
        {codes && <RecoveryCodes codes={codes} kept={kept} onKept={setKept} />}
        <FieldProblem text={problem} />
        <DialogFooter>
          {codes ? (
            <Button disabled={!kept} onClick={() => setOpen(false)}>
              Done
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button disabled={busy} onClick={() => void make()}>
                {busy && <Spinner />}
                Make new codes
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
