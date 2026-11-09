// The workspace's AI agent: whether it's on, which provider and model it
// runs on, what it may spend a month, and whether it can search the
// workspace's documents. These are the agent's own settings, kept beside
// the workspace's other AI settings. Keys are write-only: once saved, only
// the last characters come back.
import { CheckIcon } from "lucide-react"
import { type FormEvent, type ReactNode, useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useAgentStatusGet,
  useAiSettingsDeleteProviderKey,
  useAiSettingsGet,
  useAiSettingsGetAgent,
  useAiSettingsPutProviderKey,
  useAiSettingsSetAgentBudget,
  useAiSettingsUpdateAgent,
} from "@/client/forge/loomplane/hooks"
import type { AgentView } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import {
  FieldProblem,
  SettingsCardFooter,
  SettingsCardHead,
  SettingsSection,
} from "@/components/settings-section"
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
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

type Provider = "inherit" | "anthropic" | "openai" | "opencompat"

const providers: {
  value: Exclude<Provider, "inherit">
  label: string
  detail: string
}[] = [
  { value: "anthropic", label: "Anthropic", detail: "Claude, with your key" },
  { value: "openai", label: "OpenAI", detail: "GPT, with your key" },
  {
    value: "opencompat",
    label: "OpenAI-compatible",
    detail: "Any endpoint that speaks the OpenAI API",
  },
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

/**
 * AgentSettings is the AI agent's section of the AI settings page. The
 * agent's budget is a share of the workspace's AI budget, so it takes what's
 * still unallocated there.
 */
export function AgentSettings({ unallocatedUsd }: { unallocatedUsd: string }) {
  const view = useQuery(useAiSettingsGetAgent)
  // One section from loading to loaded, so its place on the page (and in
  // a screen reader) holds while the settings arrive.
  return (
    <AgentSection
      status={view.data ? (view.data.enabled ? "on" : "off") : undefined}
    >
      {view.status === "error" ? (
        <QueryError
          error={view.error}
          fallback="The agent's settings couldn't be loaded."
          retry={view.refetch}
        />
      ) : !view.data ? (
        <Skeleton
          data-testid="agent-settings-loading"
          className="h-72 w-full rounded-[14px]"
        />
      ) : (
        // The form starts again from what the server has after every save.
        <AgentForm
          key={view.data.revision}
          view={view.data}
          unallocatedUsd={unallocatedUsd}
          reload={() => view.refetch()}
        />
      )}
    </AgentSection>
  )
}

function AgentForm({
  view,
  unallocatedUsd,
  reload,
}: {
  view: AgentView
  unallocatedUsd: string
  reload: () => Promise<AgentView>
}) {
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

  const saved = (view.key?.type as Provider | undefined) ?? "inherit"
  const [provider, setProvider] = useState<Provider>(saved)
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

  // Reads the settings again, so the form starts over from what's saved,
  // and the agent's status, so the sidebar and the dock follow the switch.
  const refresh = async () => {
    await reload()
    invalidate(useAiSettingsGetAgent)
    invalidate(useAgentStatusGet)
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

  // The agent's own share counts as free: raising it only takes the rest.
  const room = Number(unallocatedUsd) + Number(view.budget.limitUsd)

  const saveBudget = async (e: FormEvent) => {
    e.preventDefault()
    setProblems({})
    setNotice("")
    const asked = Number(budget.trim() || "0")
    if (Number.isFinite(asked) && asked > room) {
      setProblems({
        budget: `The agent can have up to $${room.toFixed(2)} of the workspace's AI budget. Raise the workspace's budget above to give it more.`,
      })
      return
    }
    try {
      await setBudget.mutateAsync({
        body: {
          monthlyBudgetUsd: budget.trim() || "0",
          revision: view.revision,
        },
      })
      // What's left of the workspace's budget changed with it.
      invalidate(useAiSettingsGet)
      await refresh()
    } catch (err) {
      fail(err, "The budget wasn't saved.", "budget")
      await reload().catch(() => undefined)
    }
  }

  const server = `${providerName[view.server.provider] ?? view.server.provider}, ${view.server.model}`
  const limit = Number(view.budget.limitUsd)
  const spent = Number(view.budget.spentUsd)

  return (
    <>
      <Panel className="gap-3 px-4 py-4 md:px-5">
        <SettingsCardHead
          title={on ? "The agent is on" : "The agent is off"}
          description={
            on
              ? "It's working your projects, within the budget below."
              : "Choose a provider and a budget, then turn it on."
          }
          action={
            <Switch
              aria-label="Turn the agent on"
              checked={on}
              disabled={busy || (!on && !view.available)}
              onCheckedChange={(v) => void toggle("enabled", v)}
            />
          }
        />
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
        <FieldProblem text={problems.enabled} />
      </Panel>

      <Panel className="gap-0 p-0">
        <form onSubmit={(e) => void saveProvider(e)} className="grid">
          <div className="grid gap-5 px-4 py-4 md:px-5">
            <SettingsCardHead
              title="Provider and model"
              description="The server's default needs no key. Bring your own to bill the agent to your account."
            />
            <fieldset className="grid gap-2">
              <legend className="sr-only">Provider</legend>
              <div className="grid gap-2 sm:grid-cols-2">
                <Choice
                  name="agent-provider"
                  checked={provider === "inherit"}
                  onChange={() => setProvider("inherit")}
                  label="Server default"
                  detail={server}
                />
                {providers.map((p) => (
                  <Choice
                    key={p.value}
                    name="agent-provider"
                    checked={provider === p.value}
                    onChange={() => setProvider(p.value)}
                    label={p.label}
                    detail={p.detail}
                  />
                ))}
              </div>
            </fieldset>

            <div className="grid gap-4 sm:grid-cols-2">
              {provider !== "inherit" && (
                <Field className={cn(hasKey && !replacing && "sm:col-span-2")}>
                  <FieldLabel htmlFor={ids.key}>API key</FieldLabel>
                  {hasKey && !replacing ? (
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="inline-flex h-8 items-center rounded-md bg-subtle px-2.5 font-mono text-[12.5px] tabular-nums">
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
                      <AlertDialog
                        open={confirming}
                        onOpenChange={setConfirming}
                      >
                        <AlertDialogTrigger
                          render={
                            <Button type="button" variant="ghost" size="sm" />
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
                      className="font-mono text-xs"
                    />
                  )}
                  <FieldProblem text={problems.key} />
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
                  />
                  <FieldProblem text={problems.baseUrl} />
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
                  className="font-mono text-xs"
                />
                <FieldProblem text={problems.model} />
              </Field>
            </div>
          </div>

          <SettingsCardFooter>
            {notice ? (
              <p
                role="status"
                className="flex items-center gap-1.5 text-[12.5px] text-overdye-ink"
              >
                <CheckIcon className="size-4 shrink-0" />
                {notice}
              </p>
            ) : (
              <p className="text-[12.5px] text-muted-foreground">
                Keys are encrypted and never shown again.
              </p>
            )}
            <Button type="submit" size="sm" disabled={busy}>
              {(update.isPending || putKey.isPending) && <Spinner />}
              Save provider
            </Button>
          </SettingsCardFooter>
        </form>
      </Panel>

      <Panel className="gap-0 p-0">
        <form onSubmit={(e) => void saveBudget(e)} className="grid">
          <div className="grid gap-4 px-4 py-4 md:px-5">
            <SettingsCardHead
              title="The agent's share of the AI budget"
              description="It comes out of the workspace's budget above. At the limit the agent stops until next month or a higher budget."
            />
            <div className="grid gap-2">
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-mono text-[22px] leading-none font-medium tracking-[-0.02em] tabular-nums">
                  ${view.budget.spentUsd}
                </span>
                <span className="text-[12.5px] text-muted-foreground">
                  {limit > 0 ? (
                    <>
                      of{" "}
                      <span className="font-mono tabular-nums">
                        ${view.budget.limitUsd}
                      </span>{" "}
                      this month
                    </>
                  ) : (
                    "spent this month, no budget set"
                  )}
                </span>
              </div>
              {limit > 0 && (
                <Progress
                  aria-label="Budget spent this month"
                  value={Math.min(100, (spent / limit) * 100)}
                />
              )}
            </div>
            <Field>
              <FieldLabel htmlFor={ids.budget}>Monthly budget (USD)</FieldLabel>
              <Input
                id={ids.budget}
                inputMode="decimal"
                value={budget}
                onChange={(e) => setBudgetText(e.target.value)}
                placeholder="For example, 25"
                aria-invalid={!!problems.budget}
                className="max-w-[200px] tabular-nums"
              />
              <FieldDescription>
                {room > 0 ? (
                  <>
                    {`Up to $${room.toFixed(2)} from the workspace's AI budget.`}
                    {Number(budget) !== room && (
                      <>
                        {" "}
                        <Button
                          type="button"
                          variant="link"
                          className="h-auto p-0 text-[12.5px]"
                          onClick={() => setBudgetText(room.toFixed(2))}
                        >
                          {`Use all $${room.toFixed(2)}`}
                        </Button>
                      </>
                    )}
                  </>
                ) : (
                  "Set the workspace's AI budget above first. The agent's comes out of it."
                )}
              </FieldDescription>
              <FieldProblem text={problems.budget} />
            </Field>
          </div>
          <SettingsCardFooter>
            <p className="text-[12.5px] text-muted-foreground">
              The agent can&apos;t be turned on without a budget.
            </p>
            <Button type="submit" size="sm" variant="outline" disabled={busy}>
              {setBudget.isPending && <Spinner />}
              Save budget
            </Button>
          </SettingsCardFooter>
        </form>
      </Panel>

      <Panel className="px-4 py-4 md:px-5">
        <SettingsCardHead
          title="Knowledge"
          description={
            view.knowledgeStatus.available
              ? "Lets the agent search your workspace's documents before it answers."
              : (view.knowledgeStatus.reason ??
                "Knowledge isn't available on this server.")
          }
          action={
            <Switch
              aria-label="Search the workspace's documents"
              checked={view.knowledge && view.knowledgeStatus.available}
              disabled={busy || !view.knowledgeStatus.available}
              onCheckedChange={(v) => void toggle("knowledge", v)}
            />
          }
        />
      </Panel>
      <FieldProblem text={problems.general} />
    </>
  )
}

const agentDescription =
  "An agent that works your projects for you. It plans and deploys the way a teammate would, and stops to ask when a change needs a person."

/** AgentSection is the agent's place on the page, loaded or not. */
function AgentSection({
  status,
  children,
}: {
  status?: "on" | "off"
  children: ReactNode
}) {
  return (
    <SettingsSection
      title="AI agent"
      description={agentDescription}
      aside={
        status && (
          <span className="mt-1.5 flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
            <span
              aria-hidden="true"
              className={cn(
                "size-1.5 rounded-full",
                status === "on" ? "bg-overdye" : "bg-muted-foreground/40"
              )}
            />
            {status === "on" ? "On" : "Off"}
          </span>
        )
      }
    >
      {children}
    </SettingsSection>
  )
}

function Choice({
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
  detail: string
}) {
  const note = useId()
  return (
    <label className="flex cursor-pointer items-start gap-2.5 rounded-[10px] px-3 py-2.5 ring-1 ring-border transition-colors hover:bg-subtle has-checked:bg-subtle has-checked:ring-2 has-checked:ring-primary has-focus-visible:ring-2 has-focus-visible:ring-ring">
      <input
        type="radio"
        name={name}
        checked={checked}
        onChange={onChange}
        aria-label={label}
        aria-describedby={note}
        className="mt-0.5 size-3.5 accent-primary"
      />
      <span className="grid min-w-0 gap-0.5">
        <span className="text-[13px] font-medium">{label}</span>
        <span id={note} className="text-[12px] text-muted-foreground">
          {detail}
        </span>
      </span>
    </label>
  )
}
