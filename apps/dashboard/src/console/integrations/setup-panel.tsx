import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@forge-go/dashboard-kit/components/alert"
import { Button } from "@forge-go/dashboard-kit/components/button"
import { Checkbox } from "@forge-go/dashboard-kit/components/checkbox"
import { ConfirmDialog } from "@forge-go/dashboard-kit/components/confirm-dialog"
import { DescriptionList } from "@forge-go/dashboard-kit/components/detail-layout"
import { Input } from "@forge-go/dashboard-kit/components/input"
import { Label } from "@forge-go/dashboard-kit/components/label"
import { NoneCell } from "@forge-go/dashboard-kit/components/none-cell"
import { CommandAlert } from "@forge-go/dashboard-kit/components/query-boundary"
import { Textarea } from "@forge-go/dashboard-kit/components/textarea"
import { Timestamp } from "@forge-go/dashboard-kit/components/timestamp"
import { ExternalLinkIcon } from "@forge-go/dashboard-kit/icons"
import { useCommand } from "@forge-go/dashboard-plugin"
import { type ReactNode, useState } from "react"

import { CopyButton, CopyValue, sourceLabel } from "./components"
import {
  type AckResponse,
  type AppCheck,
  type ConsoleField,
  type IntegrationDetail,
  type SecretName,
  initialValues,
  keepsSecret,
  parseCredentials,
  replacesApp,
} from "./types"

/**
 * Everything it takes to set one integration's app up, in the order an
 * operator does it: register the app with the provider, allow the callback
 * URL and scopes, paste the client back, check it, save. Used in the list's
 * side sheet and on the integration's own page.
 */
export function AppSetup({
  integration,
  onSaved,
}: {
  integration: IntegrationDetail
  onSaved?: () => void
}) {
  if (!integration.editable) {
    return <FixedApp integration={integration} />
  }
  return <EditableSetup integration={integration} onSaved={onSaved} />
}

function EditableSetup({
  integration: it,
  onSaved,
}: {
  integration: IntegrationDetail
  onSaved?: () => void
}) {
  const [clientId, setClientId] = useState(it.clientId ?? "")
  const [clientSecret, setClientSecret] = useState("")
  const [values, setValues] = useState(() => initialValues(it))
  const [clearWebhook, setClearWebhook] = useState(false)
  const [pasted, setPasted] = useState(false)
  const [saved, setSaved] = useState(false)
  const [confirming, setConfirming] = useState(false)

  const save = useCommand<AckResponse>("oauth.save")
  const check = useCommand<AppCheck>("oauth.check")

  const fields = it.provider.fields ?? []
  const identity = { clientId, appId: values.appId }
  const keeps = (name: SecretName) => keepsSecret(it, name, identity)
  // A field is filled when it has a value, keeps a saved one, or may be
  // left out.
  const filled = (f: ConsoleField) =>
    f.optional ||
    (values[f.name] ?? "").trim() !== "" ||
    (isSecret(f) && keeps(f.name as SecretName))
  const canSave =
    clientId.trim() !== "" &&
    (keeps("clientSecret") || clientSecret.trim() !== "") &&
    fields.every(filled)
  const replacing = replacesApp(it, identity)
  const warning = changeWarning(it)

  async function submit() {
    setSaved(false)
    const res = await save.execute({
      key: it.key,
      clientId,
      clientSecret,
      ...values,
      clearWebhookSecret: clearWebhook || undefined,
    })
    if (res === undefined) {
      return
    }
    setClientSecret("")
    setValues((v) => {
      const next = { ...v }
      for (const f of fields) {
        if (isSecret(f)) next[f.name] = ""
      }
      return next
    })
    setClearWebhook(false)
    setConfirming(false)
    setSaved(true)
    onSaved?.()
  }

  // Google hands out a credentials JSON; pasting it fills both fields.
  function onPasteId(text: string): boolean {
    const creds = parseCredentials(text)
    if (!creds) {
      return false
    }
    setClientId(creds.clientId)
    if (creds.clientSecret) {
      setClientSecret(creds.clientSecret)
    }
    setPasted(true)
    return true
  }

  const scopes = it.provider.scopes ?? []
  const pasteStep = scopes.length > 0 ? 4 : 3

  return (
    <form
      className="flex min-w-0 flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault()
        if (!canSave) {
          return
        }
        if (replacing) {
          setConfirming(true)
          return
        }
        submit().catch(() => undefined)
      }}
    >
      <ol className="flex flex-col gap-5">
        <ProviderStep integration={it} />
        <Step n={2} title="Allow this callback URL">
          <CopyValue label="callback URL" value={it.callbackUrl} />
          <p className="text-xs text-muted-foreground">
            It has to match exactly, down to https and the host.
          </p>
        </Step>
        {scopes.length > 0 && <ScopesStep scopes={scopes} />}

        <Step
          n={pasteStep}
          title={it.clientId ? "App" : "Paste the app's details"}
        >
          <CommandAlert error={save.error} title="Couldn't save the app" />
          <Field
            hint={
              pasted
                ? "Filled in from the credentials you pasted."
                : "You can paste a downloaded credentials JSON here; both fields fill in."
            }
            id={`client-id-${it.key}`}
            label="Client ID"
          >
            <Input
              autoComplete="off"
              id={`client-id-${it.key}`}
              onChange={(e) => setClientId(e.target.value)}
              onPaste={(e) => {
                if (onPasteId(e.clipboardData.getData("text"))) {
                  e.preventDefault()
                }
              }}
              spellCheck={false}
              value={clientId}
            />
          </Field>
          <Field
            hint={
              it.source === "platform" && !keeps("clientSecret")
                ? "A new client ID needs its own secret."
                : undefined
            }
            id={`client-secret-${it.key}`}
            label="Client secret"
          >
            <SecretInput
              id={`client-secret-${it.key}`}
              keeps={keeps("clientSecret")}
              onChange={setClientSecret}
              value={clientSecret}
            />
          </Field>
          {fields.map((f) => (
            <ExtraField
              clear={f.name === "webhookSecret" ? clearWebhook : undefined}
              field={f}
              id={`${f.name}-${it.key}`}
              key={f.name}
              keeps={isSecret(f) && keeps(f.name as SecretName)}
              onChange={(v) => setValues((cur) => ({ ...cur, [f.name]: v }))}
              onClear={setClearWebhook}
              value={values[f.name] ?? ""}
            />
          ))}

          {replacing && (
            <Alert variant="destructive">
              <AlertTitle>Connections will have to be made again</AlertTitle>
              <AlertDescription>{warning}</AlertDescription>
            </Alert>
          )}
          <CheckOutcome error={check.error} result={check.data} />
          {saved && !save.error && (
            <Alert>
              <AlertTitle>Saved</AlertTitle>
              <AlertDescription>
                It's live at once here, with no restart; the analysis worker
                picks it up within 30 seconds.
              </AlertDescription>
            </Alert>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={!canSave || save.loading} type="submit">
              {save.loading ? "Saving…" : "Save"}
            </Button>
            <Button
              disabled={
                check.loading ||
                clientId.trim() === "" ||
                (!keeps("clientSecret") && clientSecret.trim() === "")
              }
              onClick={() => {
                check
                  .execute({
                    key: it.key,
                    clientId,
                    clientSecret,
                    appId: values.appId,
                    privateKey: values.privateKey,
                  })
                  .catch(() => undefined)
              }}
              type="button"
              variant="outline"
            >
              {check.loading ? "Checking…" : `Check with ${it.provider.name}`}
            </Button>
            <span className="flex-1" />
            {it.source === "platform" && <RemoveApp integration={it} />}
          </div>
          <LastChanged integration={it} />
        </Step>
      </ol>
      <ConfirmDialog
        confirmLabel="Save anyway"
        description={warning}
        onConfirm={() => {
          submit().catch(() => undefined)
        }}
        onOpenChange={(o) => !o && setConfirming(false)}
        open={confirming}
        pending={save.loading}
        title={`Replace ${it.name}'s app?`}
      >
        <CommandAlert error={save.error} title="Couldn't save the app" />
      </ConfirmDialog>
    </form>
  )
}

function isSecret(f: ConsoleField): boolean {
  return f.kind === "secret" || f.kind === "pem"
}

/** What replacing or removing the app breaks, in a sentence or two. */
function changeWarning(it: IntegrationDetail): string {
  if (it.provider.changeWarning) {
    return it.provider.changeWarning
  }
  const n = it.connections?.total ?? 0
  const orgs = it.organizations
  const what =
    n > 0
      ? `${n} ${n === 1 ? "connection" : "connections"} in `
      : "Sign-ins in "
  return `${what}${orgs} ${orgs === 1 ? "organization" : "organizations"} signed in with the current app, and each will need to sign in again. Changing only the secret keeps them all working.`
}

/** One of the provider's extra fields. */
function ExtraField({
  field: f,
  id,
  value,
  keeps,
  clear,
  onChange,
  onClear,
}: {
  field: ConsoleField
  id: string
  value: string
  keeps: boolean
  clear?: boolean
  onChange: (v: string) => void
  onClear: (v: boolean) => void
}) {
  let input: ReactNode
  if (f.kind === "pem") {
    input = (
      <Textarea
        autoComplete="off"
        className="min-h-24 font-mono text-xs"
        id={id}
        onChange={(e) => onChange(e.target.value)}
        placeholder={
          keeps
            ? "Saved. Leave blank to keep it"
            : "-----BEGIN RSA PRIVATE KEY-----"
        }
        spellCheck={false}
        value={value}
      />
    )
  } else if (f.kind === "secret") {
    input = (
      <SecretInput
        disabled={clear}
        id={id}
        keeps={keeps}
        onChange={onChange}
        value={value}
      />
    )
  } else {
    input = (
      <Input
        autoComplete="off"
        id={id}
        inputMode={f.kind === "number" ? "numeric" : undefined}
        onChange={(e) => onChange(e.target.value)}
        spellCheck={false}
        value={value}
      />
    )
  }
  return (
    <Field
      hint={f.hint}
      id={id}
      label={f.optional ? `${f.label} (optional)` : f.label}
    >
      {input}
      {f.name === "webhookSecret" && keeps && (
        <Label className="flex items-center gap-2 font-normal">
          <Checkbox
            checked={clear === true}
            onCheckedChange={(v) => onClear(v === true)}
          />
          Remove the saved webhook secret
        </Label>
      )}
    </Field>
  )
}

function SecretInput({
  id,
  value,
  keeps,
  disabled,
  onChange,
}: {
  id: string
  value: string
  keeps: boolean
  disabled?: boolean
  onChange: (v: string) => void
}) {
  return (
    <Input
      autoComplete="new-password"
      disabled={disabled}
      id={id}
      onChange={(e) => onChange(e.target.value)}
      placeholder={keeps ? "Saved. Leave blank to keep it" : ""}
      spellCheck={false}
      type="password"
      value={value}
    />
  )
}

function ProviderStep({ integration: it }: { integration: IntegrationDetail }) {
  const p = it.provider
  const notes = p.notes ?? []
  return (
    <Step n={1} title={`Register an app with ${p.name}`}>
      <span className="flex flex-wrap gap-2">
        {p.consoleUrl && (
          <Button
            render={
              <a
                href={p.consoleUrl}
                rel="noopener noreferrer"
                target="_blank"
              />
            }
            size="sm"
            variant="outline"
          >
            Open {p.name}
            <ExternalLinkIcon />
          </Button>
        )}
        {p.docsUrl && (
          <Button
            render={
              <a href={p.docsUrl} rel="noopener noreferrer" target="_blank" />
            }
            size="sm"
            variant="ghost"
          >
            Loomplane's guide
            <ExternalLinkIcon />
          </Button>
        )}
      </span>
      {notes.length > 0 && (
        <ul className="flex list-disc flex-col gap-1 pl-4 text-muted-foreground">
          {notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </Step>
  )
}

function ScopesStep({ scopes }: { scopes: string[] }) {
  return (
    <Step
      action={
        <CopyButton label="Copy all scopes" text={scopes.join(" ")}>
          Copy all
        </CopyButton>
      }
      n={3}
      title={
        scopes.length === 1
          ? "Allow this scope"
          : `Allow these ${scopes.length} scopes`
      }
    >
      <span className="flex flex-wrap gap-1">
        {scopes.map((sc) => (
          <code
            className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs break-all"
            key={sc}
          >
            {sc}
          </code>
        ))}
      </span>
    </Step>
  )
}

function Step({
  n,
  title,
  action,
  children,
}: {
  n: number
  title: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <li className="flex gap-3">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium">
        {n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-2.5 text-sm">
        <div className="flex min-h-6 items-center justify-between gap-2">
          <span className="font-medium">{title}</span>
          {action}
        </div>
        {children}
      </div>
    </li>
  )
}

const OUTCOME_TITLE: Record<AppCheck["result"], string> = {
  accepted: "The provider accepted this app",
  rejected: "The provider rejected this app",
  redirect_mismatch: "The callback URL isn't allowed yet",
  unreachable: "Couldn't reach the provider",
  inconclusive: "No clear answer",
}

export function CheckOutcome({
  result,
  error,
}: {
  result?: AppCheck
  error?: { code: string; message: string }
}) {
  if (error) {
    return <CommandAlert error={error} title="Couldn't check the app" />
  }
  if (!result) {
    return null
  }
  return (
    <Alert
      variant={
        result.result === "rejected" || result.result === "redirect_mismatch"
          ? "destructive"
          : "default"
      }
    >
      <AlertTitle>{OUTCOME_TITLE[result.result]}</AlertTitle>
      <AlertDescription>
        <p>{result.summary}</p>
        {result.detail && (
          <p className="mt-1 font-mono text-xs">{result.detail}</p>
        )}
      </AlertDescription>
    </Alert>
  )
}

/** An app set in configuration: what it is, and a check for clouds. */
function FixedApp({ integration: it }: { integration: IntegrationDetail }) {
  const check = useCommand<AppCheck>("oauth.check")
  const configured = it.source === "config"
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p className="text-muted-foreground">
        {configured
          ? `Set in Loomplane's configuration (${it.configKey}), which wins over this page.`
          : `Not set. ${it.name} is set in Loomplane's configuration (${it.configKey}) only.`}{" "}
        {it.kind === "repository"
          ? "Change it there and restart loomplane and loomplane-worker."
          : "Change it there and restart loomplane, or remove it there to manage it here."}
      </p>
      <DescriptionList
        items={[
          {
            term: "Client ID",
            value: it.clientId ? (
              <span className="font-mono text-xs">{it.clientId}</span>
            ) : (
              <NoneCell label="client ID" />
            ),
          },
          ...(it.appId ? [{ term: "App ID", value: String(it.appId) }] : []),
          ...(it.slug ? [{ term: "Slug", value: it.slug }] : []),
          {
            term: "Callback URL",
            value: <CopyValue label="callback URL" value={it.callbackUrl} />,
          },
          { term: "Set in", value: sourceLabel(it.source) },
        ]}
      />
      {configured && (
        <>
          <CheckOutcome error={check.error} result={check.data} />
          <Button
            className="self-start"
            disabled={check.loading}
            onClick={() => {
              check.execute({ key: it.key }).catch(() => undefined)
            }}
            type="button"
            variant="outline"
          >
            {check.loading ? "Checking…" : `Check with ${it.provider.name}`}
          </Button>
        </>
      )}
    </div>
  )
}

function LastChanged({ integration: it }: { integration: IntegrationDetail }) {
  if (!it.updatedAt) {
    return null
  }
  return (
    <p className="text-xs text-muted-foreground">
      Last changed <Timestamp label="change" value={it.updatedAt} />
      {it.updatedBy ? ` by ${it.updatedBy}` : ""}.
    </p>
  )
}

function RemoveApp({ integration: it }: { integration: IntegrationDetail }) {
  const [open, setOpen] = useState(false)
  const remove = useCommand<AckResponse>("oauth.remove")

  async function confirm() {
    const result = await remove.execute({ key: it.key })
    if (result !== undefined) {
      setOpen(false)
    }
  }

  const instead =
    it.kind === "cloud"
      ? `People can't sign in to ${it.name} any more; they connect with a token instead.`
      : `Organizations can't connect ${it.name} any more, and only public repositories are read there.`
  const description = replacesApp(it, null)
    ? `${instead} ${changeWarning(it)}`
    : `${instead}${it.outlivesApp ? " Existing connections keep working." : ""}`

  return (
    <>
      <Button
        onClick={() => {
          remove.reset()
          setOpen(true)
        }}
        type="button"
        variant="ghost"
      >
        Remove app
      </Button>
      <ConfirmDialog
        confirmLabel="Remove"
        description={description}
        onConfirm={() => {
          confirm().catch(() => undefined)
        }}
        onOpenChange={(o) => !o && setOpen(false)}
        open={open}
        pending={remove.loading}
        title={`Remove ${it.name}'s app?`}
      >
        <CommandAlert error={remove.error} title="Couldn't remove it" />
      </ConfirmDialog>
    </>
  )
}

function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}
