// The workspace's AI agent: whether it's on, which provider and model it
// runs on, what it may spend a month, and whether it can search the
// workspace's documents. These are the agent's own settings, kept beside
// the workspace's other AI settings. Keys are write-only: once saved, only
// the last characters come back.
import { CheckIcon, SparklesIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useAiSettingsDeleteProviderKey,
  useAiSettingsGetAgent,
  useAiSettingsPutProviderKey,
  useAiSettingsSetAgentBudget,
  useAiSettingsUpdateAgent,
} from "@/client/forge/loomplane/hooks"
import type { AgentView } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"

type Choice = "inherit" | "anthropic" | "openai" | "opencompat"

const providers: { value: Exclude<Choice, "inherit">; label: string }[] = [
  { value: "anthropic", label: "Anthropic" },
  { value: "openai", label: "OpenAI" },
  { value: "opencompat", label: "OpenAI-compatible" },
]

const providerName: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  "openai-compatible": "OpenAI-compatible",
  opencompat: "OpenAI-compatible",
}

type FieldName = "enabled" | "provider" | "key" | "baseUrl" | "model" | "budget"
type Problems = Partial<Record<FieldName | "general", string>>

/**
 * fieldFor says which field an API message is about, so it can show under
 * that field. The agent's settings errors name what's wrong in words.
 */
export function fieldFor(message: string): FieldName | "general" {
  const m = message.toLowerCase()
  if (m.includes("budget")) return "budget"
  if (m.includes("base_url") || m.includes("base url")) return "baseUrl"
  if (m.includes("model") || m.includes("price")) return "model"
  if (m.includes("key")) return "key"
  return "general"
}

/** AgentSettings is the AI agent's panel on the AI settings page. */
export function AgentSettings() {
  const view = useQuery(useAiSettingsGetAgent)
  if (view.status === "error")
    return (
      <QueryError
        error={view.error}
        fallback="The agent's settings couldn't be loaded."
        retry={view.refetch}
      />
    )
  if (!view.data)
    return (
      <Skeleton
        data-testid="agent-settings-loading"
        className="h-72 w-full rounded-[14px]"
      />
    )
  // The form starts again from what the server has after every save.
  return (
    <AgentForm
      key={view.data.revision}
      view={view.data}
      reload={() => view.refetch()}
    />
  )
}

function AgentForm({
  view,
  reload,
}: {
  view: AgentView
  reload: () => Promise<AgentView>
}) {
  const heading = useId()
  const ids = {
    key: useId(),
    base: useId(),
    model: useId(),
    budget: useId(),
  }
  const update = useMutation(useAiSettingsUpdateAgent)
  const putKey = useMutation(useAiSettingsPutProviderKey)
  const delKey = useMutation(useAiSettingsDeleteProviderKey)
  const setBudget = useMutation(useAiSettingsSetAgentBudget)
  const invalidate = useInvalidate()

  const saved = (view.key?.type as Choice | undefined) ?? "inherit"
  const [provider, setProvider] = useState<Choice>(saved)
  const [apiKey, setApiKey] = useState("")
  const [replacing, setReplacing] = useState(false)
  const [baseUrl, setBaseUrl] = useState(view.effective.baseUrl ?? "")
  const [model, setModel] = useState(view.effective.model)
  const [budget, setBudgetText] = useState(
    Number(view.budget.limitUsd) > 0 ? view.budget.limitUsd : ""
  )
  const [problems, setProblems] = useState<Problems>({})
  const [notice, setNotice] = useState("")
  const [confirming, setConfirming] = useState(false)

  const busy =
    update.isPending ||
    putKey.isPending ||
    delKey.isPending ||
    setBudget.isPending
  const hasKey = !!view.key && view.key.type === provider
  const on = view.enabled

  // fail files an API message under the field it is about.
  const fail = (err: unknown, fallback: string, field?: FieldName) => {
    const message = messageOf(err, fallback)
    const where =
      field && statusOf(err) !== 422
        ? field
        : statusOf(err) === 422
          ? fieldFor(message)
          : "general"
    setProblems({ [where]: message })
  }

  // Reads the settings again, so the form starts over from what's saved.
  const refresh = async () => {
    await reload()
    invalidate(useAiSettingsGetAgent)
  }

  const toggle = async (key: "enabled" | "knowledge", value: boolean) => {
    setProblems({})
    setNotice("")
    try {
      await update.mutateAsync({
        body: { [key]: value, revision: view.revision },
      })
      await refresh()
    } catch (err) {
      fail(
        err,
        "The change wasn't saved.",
        key === "enabled" ? "enabled" : undefined
      )
      await reload().catch(() => undefined)
    }
  }

  const saveProvider = async (e: FormEvent) => {
    e.preventDefault()
    setProblems({})
    setNotice("")
    let revision = view.revision
    try {
      if (provider !== "inherit") {
        const fields: Record<string, string> = {}
        if (apiKey.trim()) fields.api_key = apiKey.trim()
        if (provider === "opencompat" && baseUrl.trim())
          fields.base_url = baseUrl.trim()
        if (Object.keys(fields).length > 0) {
          await putKey.mutateAsync({
            path: { name: provider },
            body: { type: provider, fields },
          })
          revision = (await reload()).revision
        }
      }
      await update.mutateAsync({
        body: {
          providerKey: provider === "inherit" ? "" : provider,
          model: model.trim(),
          revision,
        },
      })
      setNotice("Saved. Loomplane never shows an API key again.")
      await refresh()
    } catch (err) {
      fail(err, "The provider wasn't saved.", "key")
      await reload().catch(() => undefined)
    } finally {
      // Never left in the page, saved or not.
      setApiKey("")
    }
  }

  const removeKey = async () => {
    setProblems({})
    setNotice("")
    try {
      // The agent can't keep running on a key that's being deleted.
      if (view.providerKey)
        await update.mutateAsync({
          body: { providerKey: "", revision: view.revision },
        })
      await delKey.mutateAsync({ path: { name: view.key?.name ?? provider } })
      await refresh()
    } catch (err) {
      fail(err, "The key wasn't removed.", "key")
      await reload().catch(() => undefined)
    }
  }

  const saveBudget = async (e: FormEvent) => {
    e.preventDefault()
    setProblems({})
    setNotice("")
    try {
      await setBudget.mutateAsync({
        body: {
          monthlyBudgetUsd: budget.trim() || "0",
          revision: view.revision,
        },
      })
      await refresh()
    } catch (err) {
      fail(err, "The budget wasn't saved.", "budget")
      await reload().catch(() => undefined)
    }
  }

  const server = `${providerName[view.server.provider] ?? view.server.provider}, ${view.server.model}`

  return (
    <Panel aria-labelledby={heading} className="gap-5 px-4 py-4 md:px-5">
      <div className="flex items-start justify-between gap-4">
        <div className="grid gap-1">
          <h2
            id={heading}
            className="flex items-center gap-2 text-[15px] font-semibold tracking-tight"
          >
            <SparklesIcon
              aria-hidden="true"
              className="size-4 text-muted-foreground"
            />
            AI agent
          </h2>
          <p className="max-w-[64ch] text-[13px] leading-relaxed text-pretty text-muted-foreground">
            An agent that works your projects for you. It plans and deploys the
            way a teammate would, and stops to ask when a change needs a person.
          </p>
        </div>
        <Switch
          aria-label="Turn the agent on"
          checked={on}
          disabled={busy || (!on && !view.available)}
          onCheckedChange={(v) => void toggle("enabled", v)}
        />
      </div>

      {!view.available && (
        <p className="text-[12.5px] text-muted-foreground">
          This server can&apos;t run the agent yet.
        </p>
      )}
      {view.problem && (
        <Alert variant="destructive">
          <AlertDescription>{view.problem}</AlertDescription>
        </Alert>
      )}
      <Problem text={problems.enabled} />

      <form onSubmit={(e) => void saveProvider(e)} className="grid gap-4">
        <fieldset className="grid gap-2">
          <legend className="mb-1 text-[13px] font-medium">Provider</legend>
          <Radio
            name="agent-provider"
            checked={provider === "inherit"}
            onChange={() => setProvider("inherit")}
            label="Server default"
            detail={server}
          />
          {providers.map((p) => (
            <Radio
              key={p.value}
              name="agent-provider"
              checked={provider === p.value}
              onChange={() => setProvider(p.value)}
              label={p.label}
            />
          ))}
        </fieldset>

        {provider !== "inherit" && (
          <Field>
            <FieldLabel htmlFor={ids.key}>API key</FieldLabel>
            {hasKey && !replacing ? (
              <div className="flex flex-wrap items-center gap-2">
                <span className="rounded-md bg-subtle px-2.5 py-1.5 font-mono text-[12.5px] tabular-nums">
                  Key set, ends in {view.key?.hint}
                </span>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setReplacing(true)}
                >
                  Replace
                </Button>
                <AlertDialog open={confirming} onOpenChange={setConfirming}>
                  <AlertDialogTrigger
                    render={
                      <Button type="button" variant="outline" size="sm" />
                    }
                  >
                    Remove
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>
                        Remove the agent&apos;s key?
                      </AlertDialogTitle>
                      <AlertDialogDescription>
                        The agent goes back to the server&apos;s default
                        provider until you add another key.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Keep it</AlertDialogCancel>
                      <AlertDialogAction
                        variant="destructive"
                        onClick={() => {
                          setConfirming(false)
                          void removeKey()
                        }}
                      >
                        Remove
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            ) : (
              <Input
                id={ids.key}
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder="Paste an API key"
                autoComplete="new-password"
                spellCheck={false}
                aria-invalid={!!problems.key}
                className="max-w-[420px] font-mono text-xs"
              />
            )}
            <Problem text={problems.key} />
          </Field>
        )}

        {provider === "opencompat" && (
          <Field>
            <FieldLabel htmlFor={ids.base}>Base URL</FieldLabel>
            <Input
              id={ids.base}
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder="https://models.example.com/v1"
              spellCheck={false}
              aria-invalid={!!problems.baseUrl}
              className="max-w-[420px]"
            />
            <Problem text={problems.baseUrl} />
          </Field>
        )}

        <Field>
          <FieldLabel htmlFor={ids.model}>Model</FieldLabel>
          <Input
            id={ids.model}
            value={model}
            onChange={(e) => setModel(e.target.value)}
            spellCheck={false}
            aria-invalid={!!problems.model}
            className="max-w-[420px] font-mono text-xs"
          />
          <Problem text={problems.model} />
        </Field>

        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={busy}>
            {(update.isPending || putKey.isPending) && <Spinner />}
            Save provider
          </Button>
          {notice && (
            <p
              role="status"
              className="flex items-center gap-1.5 text-[13px] text-overdye-ink"
            >
              <CheckIcon className="size-4 shrink-0" />
              {notice}
            </p>
          )}
        </div>
      </form>

      <form onSubmit={(e) => void saveBudget(e)} className="grid gap-3">
        <Field>
          <FieldLabel htmlFor={ids.budget}>Monthly budget (USD)</FieldLabel>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              id={ids.budget}
              inputMode="decimal"
              value={budget}
              onChange={(e) => setBudgetText(e.target.value)}
              placeholder="For example, 25"
              aria-invalid={!!problems.budget}
              className="max-w-[200px] tabular-nums"
            />
            <Button type="submit" variant="outline" disabled={busy}>
              {setBudget.isPending && <Spinner />}
              Save budget
            </Button>
          </div>
          <FieldDescription>
            Spent this month:{" "}
            <span className="font-mono tabular-nums">
              ${view.budget.spentUsd}
            </span>
            {Number(view.budget.limitUsd) > 0 && (
              <>
                {" "}
                of{" "}
                <span className="font-mono tabular-nums">
                  ${view.budget.limitUsd}
                </span>
              </>
            )}
            . At the limit the agent stops until next month or a higher budget.
          </FieldDescription>
          <Problem text={problems.budget} />
        </Field>
      </form>

      <div className="flex items-start justify-between gap-4 border-t border-border pt-4">
        <div className="grid gap-1">
          <span className="text-[13px] font-medium">Knowledge</span>
          <p className="max-w-[56ch] text-[12.5px] leading-normal text-muted-foreground">
            {view.knowledgeStatus.available
              ? "Lets the agent search your workspace's documents before it answers."
              : (view.knowledgeStatus.reason ??
                "Knowledge isn't available on this server.")}
          </p>
        </div>
        <Switch
          aria-label="Search the workspace's documents"
          checked={view.knowledge && view.knowledgeStatus.available}
          disabled={busy || !view.knowledgeStatus.available}
          onCheckedChange={(v) => void toggle("knowledge", v)}
        />
      </div>
      <Problem text={problems.general} />
    </Panel>
  )
}

function Problem({ text }: { text?: string }) {
  if (!text) return null
  return (
    <p role="alert" className="text-[12.5px] text-destructive">
      {text}
    </p>
  )
}

function Radio({
  name,
  checked,
  onChange,
  label,
  detail,
}: {
  name: string
  checked: boolean
  onChange: () => void
  label: string
  detail?: string
}) {
  return (
    <label className="flex cursor-pointer items-baseline gap-2 text-[13px]">
      <input
        type="radio"
        name={name}
        checked={checked}
        onChange={onChange}
        className="size-3.5 translate-y-0.5 accent-primary"
      />
      <span>{label}</span>
      {detail && (
        <span className="text-[12.5px] text-muted-foreground">{detail}</span>
      )}
    </label>
  )
}
