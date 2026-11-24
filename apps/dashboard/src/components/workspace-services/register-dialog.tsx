// Registering a workspace service: paste where it is, check it from one of
// your servers, then say what it is and who may use it. Nothing is stored
// until the check passes, and the connection string goes to the server
// only to be checked and kept in Vault.
import { ArrowLeftIcon, DatabaseIcon, EyeIcon, EyeOffIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useCatalogEndpointsList,
  useCatalogEndpointsProbe,
  useCatalogEndpointsRegister,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  CatalogconnectEndpoint,
  CheckResult,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import {
  CheckFrom,
  CheckReport,
  useCheckTargets,
} from "@/components/workspace-services/check-parts"
import {
  buildConnection,
  connectionEngines,
  type ConnectionFields,
  connectionHints,
  connectionsIn,
  defaultPort,
  engineTitles,
  type FoundConnection,
  maskConnection,
  parseConnection,
  suggestName,
} from "@/lib/connection-string"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

type Step = "connect" | "check" | "share"

const steps: { id: Step; label: string }[] = [
  { id: "connect", label: "Connect" },
  { id: "check", label: "Check" },
  { id: "share", label: "Share" },
]

export const engineLogos: Record<string, ProductKey | undefined> = {
  postgres: "postgresql",
  mysql: "mysql",
  redis: "redis",
}

const labels = ["production", "staging", "development"]

/** RegisterServiceDialog adds a workspace service by its connection string. */
export function RegisterServiceDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [dirty, setDirty] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const close = (next: boolean) => {
    if (!next && dirty) {
      setConfirming(true)
      return
    }
    setConfirming(false)
    onOpenChange(next)
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="grid max-h-[90vh] grid-rows-[auto_minmax(0,1fr)] gap-4 overflow-hidden sm:max-w-2xl">
        {open && (
          <Body
            onDirty={setDirty}
            onDone={() => {
              setDirty(false)
              onOpenChange(false)
            }}
            confirming={confirming}
            onKeepEditing={() => setConfirming(false)}
            onDiscard={() => {
              setDirty(false)
              setConfirming(false)
              onOpenChange(false)
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Body({
  onDirty,
  onDone,
  confirming,
  onKeepEditing,
  onDiscard,
}: {
  onDirty: (dirty: boolean) => void
  onDone: () => void
  confirming: boolean
  onKeepEditing: () => void
  onDiscard: () => void
}) {
  const ids = useId()
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const probe = useMutation(useCatalogEndpointsProbe)
  const register = useMutation(useCatalogEndpointsRegister)
  const projects = useQuery(useProjectsList)
  const { targets } = useCheckTargets()

  const [step, setStep] = useState<Step>("connect")
  const [mode, setMode] = useState<"paste" | "fields">("paste")
  const [dsn, setDsnRaw] = useState("")
  const [shown, setShown] = useState(false)
  const [found, setFound] = useState<FoundConnection[]>([])
  const [fields, setFields] = useState<ConnectionFields>({
    engine: "postgres",
    host: "",
    port: "",
    user: "",
    password: "",
    database: "",
    tls: "require",
  })
  const [target, setTarget] = useState("")
  const [check, setCheck] = useState<{
    dsn: string
    target: string
    result: CheckResult
  }>()
  const [error, setError] = useState("")
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [label, setLabel] = useState("production")
  const [restricted, setRestricted] = useState(false)
  const [picked, setPicked] = useState<string[]>([])

  const connection = mode === "fields" ? buildConnection(fields) : dsn.trim()
  const parts = parseConnection(connection)
  const hints = mode === "paste" ? connectionHints(connection) : []
  const from = target || targets[0]?.connection.id || ""
  const checked =
    check && check.dsn === connection && check.target === from
      ? check.result
      : undefined

  const setDsn = (next: string) => {
    setDsnRaw(next)
    setError("")
    onDirty(next.trim() !== "")
  }
  const setField = (change: Partial<ConnectionFields>) => {
    setFields((f) => ({ ...f, ...change }))
    setError("")
    onDirty(true)
  }

  // A paste of several .env lines offers the connection strings in it.
  const onPaste = (text: string) => {
    const list = connectionsIn(text)
    if (list.length > 1) {
      setFound(list)
      return true
    }
    if (list.length === 1) {
      setDsn(list[0].value)
      setFound([])
      return true
    }
    return false
  }

  const runCheck = async () => {
    if (!parts || !from) return
    setError("")
    try {
      const result = await probe.mutateAsync({
        body: { engine: parts.engine, dsn: connection, target: from },
      })
      setCheck({ dsn: connection, target: from, result: result as CheckResult })
    } catch (cause) {
      setError(
        statusOf(cause) === 429
          ? messageOf(cause, "Too many checks just now. Try again in a minute.")
          : messageOf(cause, "The check didn't run.")
      )
    }
  }

  const goShare = () => {
    if (parts && !name) setName(suggestName(parts))
    setStep("share")
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!parts) return
    setError("")
    try {
      const created = (await register.mutateAsync({
        body: {
          name: name.trim(),
          description: description.trim(),
          environmentLabel: label.trim(),
          engine: parts.engine,
          dsn: connection,
          target: from,
          restricted,
          projects: restricted ? picked : [],
        },
      })) as CatalogconnectEndpoint
      invalidate(useCatalogEndpointsList)
      toast.success(`Registered ${created.name}`, {
        description: restricted
          ? `${picked.length === 1 ? "One project" : `${picked.length} projects`} can use it now.`
          : "Every project in the workspace can use it now.",
      })
      onDone()
      void navigate({
        to: "/services/registered/$id",
        params: { id: created.id },
      })
    } catch (cause) {
      setError(messageOf(cause, "It wasn't registered."))
    }
  }

  const at = steps.findIndex((s) => s.id === step)
  const back = at > 0 ? () => setStep(steps[at - 1].id) : undefined

  return (
    <>
      <DialogHeader>
        <div className="flex items-start gap-2">
          {back && (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Back"
              onClick={back}
            >
              <ArrowLeftIcon />
            </Button>
          )}
          <div className="grid min-w-0 gap-1">
            <DialogTitle>Register a service you already run</DialogTitle>
            <DialogDescription>
              A database or cache outside Loomplane, shared with your projects.
              The connection string is kept in Vault and never shown again.
            </DialogDescription>
          </div>
        </div>
        <ol
          aria-label="Steps"
          className="mt-2 flex items-center gap-2 text-[12.5px]"
        >
          {steps.map((s, i) => (
            <li
              key={s.id}
              aria-current={s.id === step ? "step" : undefined}
              className={cn(
                "flex items-center gap-1.5",
                i < at
                  ? "text-foreground"
                  : i === at
                    ? "font-medium text-foreground"
                    : "text-muted-foreground"
              )}
            >
              <span
                className={cn(
                  "flex size-5 items-center justify-center rounded-full font-mono text-[11px]",
                  i <= at
                    ? "bg-primary text-primary-foreground"
                    : "ring-1 ring-border"
                )}
              >
                {i + 1}
              </span>
              {s.label}
              {i < steps.length - 1 && (
                <span aria-hidden="true" className="ml-1 h-px w-6 bg-border" />
              )}
            </li>
          ))}
        </ol>
      </DialogHeader>

      <form
        onSubmit={(e) => void submit(e)}
        className="-mx-1 grid min-h-0 content-start gap-4 overflow-y-auto px-1"
      >
        {step === "connect" && (
          <FieldGroup>
            {mode === "paste" ? (
              <Field>
                <FieldLabel htmlFor={`${ids}-dsn`}>
                  Connection string
                </FieldLabel>
                <div className="flex gap-2">
                  <Input
                    id={`${ids}-dsn`}
                    type={shown ? "text" : "password"}
                    className="font-mono"
                    value={dsn}
                    autoFocus
                    // "off" doesn't stop a browser filling in a saved password.
                    autoComplete="new-password"
                    spellCheck={false}
                    placeholder="postgres://user:password@db.example.com:5432/app?sslmode=require"
                    onPaste={(e) => {
                      if (onPaste(e.clipboardData.getData("text")))
                        e.preventDefault()
                    }}
                    onChange={(e) => setDsn(e.target.value)}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={
                      shown
                        ? "Hide the connection string"
                        : "Show the connection string"
                    }
                    onClick={() => setShown((s) => !s)}
                  >
                    {shown ? <EyeOffIcon /> : <EyeIcon />}
                  </Button>
                </div>
                <FieldDescription>
                  Paste a URL such as postgres://, mysql://, redis:// or
                  mongodb://, or lines from a .env file.{" "}
                  <button
                    type="button"
                    className="text-foreground underline underline-offset-4"
                    onClick={() => {
                      if (parts)
                        setFields({
                          engine: parts.engine,
                          host: parts.host,
                          port: String(parts.port),
                          user: parts.user,
                          password: "",
                          database: parts.database,
                          tls: parts.tls ?? "require",
                        })
                      setMode("fields")
                    }}
                  >
                    Enter the details instead
                  </button>
                </FieldDescription>
              </Field>
            ) : (
              <DetailFields
                ids={ids}
                fields={fields}
                onChange={setField}
                onPaste={() => setMode("paste")}
              />
            )}

            {found.length > 1 && (
              <div className="grid gap-2 rounded-lg bg-muted/50 p-3">
                <p className="text-[13px] font-medium">
                  That has {found.length} connection strings. Which one?
                </p>
                <div className="grid gap-1">
                  {found.map((f) => (
                    <button
                      key={f.key + f.value}
                      type="button"
                      onClick={() => {
                        setDsn(f.value)
                        setFound([])
                      }}
                      className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] hover:bg-background"
                    >
                      <span className="font-mono font-medium">{f.key}</span>
                      <span className="truncate font-mono text-[12px] text-muted-foreground">
                        {maskConnection(f.value)}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )}

            {parts && <ReadsAs parts={parts} />}
            {hints.length > 0 && (
              <ul className="grid gap-1.5">
                {hints.map((h) => (
                  <li
                    key={h.message}
                    className="flex flex-wrap items-center gap-2 text-[12.5px] text-weld-ink"
                  >
                    {h.message}
                    {h.fix && (
                      <Button
                        type="button"
                        size="xs"
                        variant="outline"
                        onClick={() => setDsn(h.fix!.dsn)}
                      >
                        {h.fix.label}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
            {connection && !parts && mode === "paste" && (
              <p className="text-[12.5px] text-muted-foreground">
                That doesn&apos;t read as a postgres://, mysql://, redis:// or
                mongodb:// URL.
              </p>
            )}
          </FieldGroup>
        )}

        {step === "check" && parts && (
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`${ids}-from`}>Check from</FieldLabel>
              <CheckFrom id={`${ids}-from`} value={from} onChange={setTarget} />
              <FieldDescription>
                Pick a server or cluster that runs your projects: what they
                reach, the check reaches.
              </FieldDescription>
            </Field>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant={checked?.ok ? "outline" : "default"}
                disabled={!from || probe.isPending}
                onClick={() => void runCheck()}
              >
                {probe.isPending && <Spinner data-icon="inline-start" />}
                {checked ? "Check again" : "Check connection"}
              </Button>
              <span className="truncate font-mono text-[12px] text-muted-foreground">
                {maskConnection(connection)}
              </span>
            </div>
            {checked && (
              <CheckReport
                engine={parts.engine}
                result={checked}
                tls={parts.tls}
              />
            )}
          </FieldGroup>
        )}

        {step === "share" && parts && (
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`${ids}-name`}>Name</FieldLabel>
              <Input
                id={`${ids}-name`}
                value={name}
                maxLength={100}
                required
                autoFocus
                onChange={(e) => setName(e.target.value)}
              />
              <FieldDescription>
                What projects pick it by, such as orders-db.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor={`${ids}-description`}>
                Description
              </FieldLabel>
              <Input
                id={`${ids}-description`}
                value={description}
                maxLength={200}
                placeholder="Optional: what it holds, or who looks after it"
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={`${ids}-label`}>
                Environment it serves
              </FieldLabel>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  id={`${ids}-label`}
                  value={label}
                  maxLength={40}
                  className="w-48"
                  onChange={(e) => setLabel(e.target.value)}
                />
                {labels
                  .filter((l) => l !== label)
                  .map((l) => (
                    <Button
                      key={l}
                      type="button"
                      size="xs"
                      variant="outline"
                      onClick={() => setLabel(l)}
                    >
                      {l}
                    </Button>
                  ))}
              </div>
            </Field>
            <Field>
              <FieldLabel>Who can use it</FieldLabel>
              <div
                role="radiogroup"
                aria-label="Who can use it"
                className="grid gap-2 sm:grid-cols-2"
              >
                {[
                  {
                    value: false,
                    title: "Every project",
                    detail:
                      "Any project in the workspace can connect a service to it.",
                  },
                  {
                    value: true,
                    title: "Only some projects",
                    detail:
                      "Only the projects you pick; the rest don't see it.",
                  },
                ].map((o) => (
                  <button
                    key={o.title}
                    type="button"
                    role="radio"
                    aria-checked={restricted === o.value}
                    onClick={() => setRestricted(o.value)}
                    className={cn(
                      "grid gap-0.5 rounded-lg px-3 py-2.5 text-left ring-1 transition-colors",
                      restricted === o.value
                        ? "bg-primary/5 ring-primary"
                        : "ring-border hover:bg-muted/50"
                    )}
                  >
                    <span className="text-[13px] font-medium">{o.title}</span>
                    <span className="text-[12px] text-muted-foreground">
                      {o.detail}
                    </span>
                  </button>
                ))}
              </div>
            </Field>
            {restricted && (
              <ProjectPicker
                projects={(projects.data?.projects ?? []).filter(
                  (p) => p.kind !== "service"
                )}
                picked={picked}
                onChange={setPicked}
              />
            )}
          </FieldGroup>
        )}

        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        {confirming ? (
          <div className="flex flex-wrap items-center justify-end gap-2 rounded-lg bg-muted/60 p-3">
            <span className="mr-auto text-[13px]">
              Discard this connection string?
            </span>
            <Button type="button" variant="outline" onClick={onKeepEditing}>
              Keep editing
            </Button>
            <Button type="button" variant="destructive" onClick={onDiscard}>
              Discard
            </Button>
          </div>
        ) : (
          <DialogFooter>
            {step === "connect" && (
              <Button
                type="button"
                disabled={!parts}
                onClick={() => setStep("check")}
              >
                Continue
              </Button>
            )}
            {step === "check" && (
              <Button type="button" disabled={!checked?.ok} onClick={goShare}>
                Continue
              </Button>
            )}
            {step === "share" && (
              <Button
                type="submit"
                disabled={
                  register.isPending ||
                  !name.trim() ||
                  (restricted && picked.length === 0)
                }
              >
                {register.isPending && <Spinner data-icon="inline-start" />}
                Register {name.trim() || "it"}
              </Button>
            )}
          </DialogFooter>
        )}
      </form>
    </>
  )
}

/** ReadsAs shows what a connection string points at, password left out. */
function ReadsAs({
  parts,
}: {
  parts: NonNullable<ReturnType<typeof parseConnection>>
}) {
  const facts: [string, string][] = [
    [
      "Host",
      parts.hosts.length > 1
        ? `${parts.host} and ${parts.hosts.length - 1} more`
        : `${parts.host}:${parts.port}`,
    ],
    ["User", parts.user || "None"],
    [
      "Database",
      parts.database || (parts.engine === "redis" ? "0" : "Its default"),
    ],
    [
      "TLS",
      parts.tls === "verify-full"
        ? "Verified"
        : parts.tls === "require"
          ? "Encrypted, not verified"
          : parts.tls === "disable"
            ? "Off"
            : "Not said",
    ],
  ]
  return (
    <div
      role="group"
      aria-label="Reads as"
      className="flex flex-wrap items-start gap-3 rounded-lg bg-muted/40 px-3 py-2.5"
    >
      <ProductIcon
        product={engineLogos[parts.engine]}
        fallback={DatabaseIcon}
        label={engineTitles[parts.engine]}
        size="sm"
      />
      <div className="grid min-w-0 flex-1 gap-1">
        <p className="text-[13px] font-medium">
          {engineTitles[parts.engine]}
          {!parts.hasPassword && parts.engine !== "redis" && (
            <span className="ml-2 text-[12px] font-normal text-weld-ink">
              No password in it
            </span>
          )}
        </p>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-[12.5px]">
          {facts.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="truncate font-mono">{v}</dd>
            </div>
          ))}
        </dl>
      </div>
    </div>
  )
}

function DetailFields({
  ids,
  fields,
  onChange,
  onPaste,
}: {
  ids: string
  fields: ConnectionFields
  onChange: (change: Partial<ConnectionFields>) => void
  onPaste: () => void
}) {
  return (
    <>
      <Field>
        <FieldLabel>Engine</FieldLabel>
        <div
          role="radiogroup"
          aria-label="Engine"
          className="flex flex-wrap gap-0.5 self-start rounded-[10px] bg-muted p-[3px]"
        >
          {connectionEngines.map((e) => (
            <button
              key={e}
              type="button"
              role="radio"
              aria-checked={e === fields.engine}
              onClick={() =>
                onChange({
                  engine: e,
                  tls: e === "redis" || e === "mongodb" ? "disable" : "require",
                })
              }
              className={cn(
                "h-7 rounded-lg px-2.5 text-[12.5px] font-medium",
                e === fields.engine
                  ? "bg-background text-foreground shadow-xs"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {engineTitles[e]}
            </button>
          ))}
        </div>
      </Field>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_110px]">
        <Field>
          <FieldLabel htmlFor={`${ids}-host`}>Host</FieldLabel>
          <Input
            id={`${ids}-host`}
            className="font-mono"
            value={fields.host}
            spellCheck={false}
            placeholder="db.example.com"
            onChange={(e) => onChange({ host: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${ids}-port`}>Port</FieldLabel>
          <Input
            id={`${ids}-port`}
            className="font-mono"
            inputMode="numeric"
            value={fields.port}
            placeholder={String(defaultPort(fields.engine))}
            onChange={(e) =>
              onChange({ port: e.target.value.replace(/\D/g, "") })
            }
          />
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${ids}-user`}>User</FieldLabel>
          <Input
            id={`${ids}-user`}
            className="font-mono"
            value={fields.user}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => onChange({ user: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${ids}-password`}>Password</FieldLabel>
          <Input
            id={`${ids}-password`}
            type="password"
            className="font-mono"
            value={fields.password}
            autoComplete="new-password"
            onChange={(e) => onChange({ password: e.target.value })}
          />
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${ids}-database`}>
            {fields.engine === "redis" ? "Database number" : "Database"}
          </FieldLabel>
          <Input
            id={`${ids}-database`}
            className="font-mono"
            value={fields.database}
            spellCheck={false}
            placeholder={fields.engine === "redis" ? "0" : "app"}
            onChange={(e) => onChange({ database: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${ids}-tls`}>TLS</FieldLabel>
          <Select
            items={[
              { label: "Verify the certificate", value: "verify-full" },
              { label: "Encrypt, don't verify", value: "require" },
              ...(fields.engine === "mysql"
                ? []
                : [{ label: "Off", value: "disable" }]),
            ]}
            value={fields.tls}
            onValueChange={(v) =>
              onChange({ tls: v as ConnectionFields["tls"] })
            }
          >
            <SelectTrigger id={`${ids}-tls`} size="sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="verify-full">
                Verify the certificate
              </SelectItem>
              <SelectItem value="require">
                Encrypt, don&apos;t verify
              </SelectItem>
              {fields.engine !== "mysql" && (
                <SelectItem value="disable">Off</SelectItem>
              )}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <FieldDescription>
        <button
          type="button"
          className="text-foreground underline underline-offset-4"
          onClick={onPaste}
        >
          Paste a connection string instead
        </button>
      </FieldDescription>
    </>
  )
}

/** ProjectPicker ticks the projects a workspace service is shared with. */
export function ProjectPicker({
  projects,
  picked,
  onChange,
}: {
  projects: { id: string; name: string }[]
  picked: string[]
  onChange: (ids: string[]) => void
}) {
  const [q, setQ] = useState("")
  const shown = projects.filter((p) =>
    p.name.toLowerCase().includes(q.trim().toLowerCase())
  )
  if (projects.length === 0)
    return (
      <p className="text-[12.5px] text-muted-foreground">
        There are no projects to share it with yet.
      </p>
    )
  return (
    <div className="grid gap-2">
      {projects.length > 6 && (
        <Input
          type="search"
          aria-label="Filter projects"
          placeholder="Filter projects"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          className="h-8"
        />
      )}
      <div className="grid max-h-56 gap-0.5 overflow-y-auto rounded-lg p-1 ring-1 ring-border">
        {shown.map((p) => {
          const on = picked.includes(p.id)
          return (
            <label
              key={p.id}
              className="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-muted/60"
            >
              <Checkbox
                checked={on}
                onCheckedChange={() =>
                  onChange(
                    on ? picked.filter((x) => x !== p.id) : [...picked, p.id]
                  )
                }
              />
              {p.name}
            </label>
          )
        })}
      </div>
      <p className="text-[12px] text-muted-foreground">
        {picked.length === 0
          ? "Pick at least one project."
          : `${picked.length} of ${projects.length} picked.`}
      </p>
    </div>
  )
}
