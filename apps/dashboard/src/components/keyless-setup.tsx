// What to set up in a cloud so it trusts Loomplane's tokens: the issuer,
// subject and audience to paste, the steps that make the trust, and the
// check that connects once the cloud accepts it. No key is involved.
import { CheckIcon, CopyIcon } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionKeylessCreate,
  useConnectionKeylessPrepare,
  useConnectionKeylessReplace,
  useConnectionKeylessSetup,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  KeylessSetup as Handout,
  Setting,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { formatDate } from "@/lib/format"

/** CopyButton copies value, with label naming what it copies. */
export function CopyButton({
  value,
  label,
  disabled,
}: {
  value: string
  label: string
  disabled?: boolean
}) {
  const [done, setDone] = useState<string | null>(null)
  const copied = done === value
  // The tick lasts two seconds from the copy, whatever the value does
  // meanwhile, so a value that changes and comes back shows no tick.
  useEffect(() => {
    if (done === null) return
    const t = window.setTimeout(() => setDone(null), 2000)
    return () => window.clearTimeout(t)
  }, [done])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setDone(value)
    } catch {
      toast.error("It wasn't copied.")
    }
  }
  return (
    <Button
      type="button"
      variant="outline"
      size="icon"
      onClick={() => void copy()}
      aria-label={label}
      disabled={disabled}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
    </Button>
  )
}

function Copyable({
  name,
  value,
  noun = name,
  disabled,
}: {
  name: string
  value: string
  noun?: string
  disabled?: boolean
}) {
  return (
    <div className="grid gap-1">
      <span className="text-[12.5px] text-muted-foreground">{name}</span>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 rounded-md bg-muted px-2 py-1.5 font-mono text-xs break-all">
          {value}
        </code>
        <CopyButton
          value={value}
          label={`Copy ${noun.toLowerCase()}`}
          disabled={disabled}
        />
      </div>
    </div>
  )
}

// Google's audience is the workload provider's name behind this prefix, so
// it follows what's typed. The other clouds' audiences are fixed.
const googleAudience = "//iam.googleapis.com/"

type Props = {
  cloud: string
  title: string
  name: string
  fields: Setting[]
  replacing?: Connection
  onConnected: (connection: Connection) => void
}

/**
 * KeylessSetup shows what to paste into the cloud, then checks it and
 * connects. For a new connection it reserves the connection's ID first, so
 * the subject it shows is the one the connection will have. With replacing
 * set it shows that connection's own setup and switches it to keyless.
 */
export function KeylessSetup(props: Props) {
  return props.replacing ? (
    <ExistingSetup {...props} replacing={props.replacing} />
  ) : (
    <ReservedSetup {...props} />
  )
}

function ExistingSetup(props: Props & { replacing: Connection }) {
  const existing = useQuery(useConnectionKeylessSetup, {
    path: { id: props.replacing.id },
  })
  if (existing.status === "error")
    return <Problem text={messageOf(existing.error, setupFailed)} />
  if (!existing.data) return <Skeleton className="h-40 w-full rounded-xl" />
  return <SetupPanel {...props} handout={existing.data} />
}

function ReservedSetup(props: Props) {
  const prepare = useMutation(useConnectionKeylessPrepare)
  const [reserved, setReserved] = useState<Handout | null>(null)
  const [problem, setProblem] = useState("")
  const asked = useRef(false)

  useEffect(() => {
    if (asked.current) return
    asked.current = true
    prepare
      .mutateAsync({ body: { cloud: props.cloud } })
      .then((h) => setReserved(h as Handout))
      .catch((err) => setProblem(messageOf(err, setupFailed)))
    // Reserve once per panel, not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (problem) return <Problem text={problem} />
  if (!reserved) return <Skeleton className="h-40 w-full rounded-xl" />
  return <SetupPanel {...props} handout={reserved} />
}

const setupFailed = "Keyless couldn't be set up."

function Problem({ text }: { text: string }) {
  return (
    <Alert variant="destructive">
      <AlertDescription>{text}</AlertDescription>
    </Alert>
  )
}

function SetupPanel({
  cloud,
  title,
  name,
  fields,
  replacing,
  onConnected,
  handout,
}: Props & { handout: Handout }) {
  const create = useMutation(useConnectionKeylessCreate)
  const replace = useMutation(useConnectionKeylessReplace)
  const invalidate = useInvalidate()
  const [values, setValues] = useState<Record<string, string>>({})
  const [error, setError] = useState("")
  const value = (key: string) =>
    values[key] ?? String(replacing?.details[key] ?? "")
  const audience =
    cloud === "gcp"
      ? googleAudience + value("workload_provider").trim()
      : (handout.audience ?? "")
  const busy = create.isPending || replace.isPending
  const ready =
    (!!replacing || name.trim() !== "") &&
    fields.every((f) => !f.required || value(f.key).trim() !== "")

  const connect = async () => {
    setError("")
    // A switch sends every field, empty ones too: the server lays them over
    // the connection's own settings, so an emptied one is cleared.
    const settings = Object.fromEntries(
      fields
        .map((f) => [f.key, value(f.key).trim()])
        .filter(([, v]) => replacing || v)
    )
    try {
      const c = replacing
        ? await replace.mutateAsync({
            path: { id: replacing.id },
            body: { settings },
          })
        : await create.mutateAsync({
            body: {
              connectionId: handout.connectionId,
              name: name.trim(),
              settings,
            },
          })
      invalidate(useConnectionsList)
      toast.success(
        c.status === "ready"
          ? `${c.name} is connected keylessly`
          : `${c.name} is connected, but missing permissions`
      )
      onConnected(c)
    } catch (err) {
      setError(messageOf(err, "The cloud didn't accept Loomplane's token."))
    }
  }

  return (
    <div className="grid gap-4">
      <p className="text-[13px] text-muted-foreground">
        Set up {title} to trust tokens from Loomplane. You won&apos;t paste a
        key or secret anywhere.
      </p>
      <div className="grid gap-3">
        <Copyable name="Issuer" value={handout.issuer} />
        <Copyable name="Subject" value={handout.subject} />
        <Copyable
          name="Audience"
          value={audience}
          disabled={audience === googleAudience}
        />
      </div>
      <ol className="grid gap-3">
        {handout.steps.map((s, i) => (
          <li key={s.title} className="grid gap-1.5">
            <span className="flex gap-1.5 text-sm font-medium">
              <span className="text-muted-foreground">{i + 1}.</span>
              <span>{s.title}</span>
            </span>
            {s.body && (
              <span className="text-[13px] text-muted-foreground">
                {s.body}
              </span>
            )}
            {s.command && (
              <div className="flex items-start gap-2">
                <code className="min-w-0 flex-1 rounded-md bg-muted px-2 py-1.5 font-mono text-xs break-all whitespace-pre-wrap">
                  {s.command}
                </code>
                <CopyButton
                  value={s.command}
                  label={`Copy command: ${s.title}`}
                />
              </div>
            )}
          </li>
        ))}
      </ol>
      {fields.length > 0 && (
        <FieldGroup>
          {fields.map((f) => (
            <Field key={f.key}>
              <FieldLabel htmlFor={`keyless-${f.key}`}>{f.label}</FieldLabel>
              <Input
                id={`keyless-${f.key}`}
                value={value(f.key)}
                onChange={(e) =>
                  setValues({ ...values, [f.key]: e.target.value })
                }
                // Enter checks and connects, as the button does. The
                // dialog's own form doesn't, since this panel replaces it.
                onKeyDown={(e) => {
                  if (e.key !== "Enter") return
                  e.preventDefault()
                  if (!busy && ready) void connect()
                }}
                autoComplete="off"
                required={f.required}
              />
            </Field>
          ))}
        </FieldGroup>
      )}
      {handout.expiresAt && !replacing && (
        <p className="text-xs text-muted-foreground">
          Reserved until {formatDate(handout.expiresAt)}. If you close this,
          you&apos;ll get a new subject and have to set up the trust again.
        </p>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div>
        <Button
          type="button"
          disabled={busy || !ready}
          onClick={() => void connect()}
        >
          {busy && <Spinner />}
          {replacing ? "Check and switch" : "Check and connect"}
        </Button>
      </div>
    </div>
  )
}
