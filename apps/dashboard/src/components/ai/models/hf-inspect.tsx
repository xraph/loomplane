// Any Hugging Face model: paste a repo, Loomplane reads it at its current
// commit and works out what it needs, and it deploys like a registry entry,
// marked untested. When Loomplane can't serve it, the server's sentence
// says why.
import { InfoIcon, TriangleAlertIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import { useAiModelsInspect } from "@/client/forge/loomplane/hooks"
import type { EntryDetail } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { ModelCard } from "@/components/ai/models/model-card"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { looksLikeToken, repoOf } from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

// settings is what the refusal offers when the fix is in AI settings: a
// link for admins, who can change them, or who to ask for everyone else.
type Refusal = { message: string; settings?: "link" | "ask" }

/**
 * said is the server's sentence as it wrote it. messageOf capitalizes, and
 * these often start with a repo, like meta-llama/..., whose case matters.
 */
function said(err: unknown, fallback: string): string {
  const raw = err instanceof Error ? err.message.trim() : ""
  if (!raw || /^HTTP \d+/.test(raw) || statusOf(err) === 500)
    return messageOf(err, fallback)
  return /[.!?]$/.test(raw) ? raw : `${raw}.`
}

/** cpuWarnings are the reasons a pasted model's CPU weights won't run. */
function cpuWarnings(detail: EntryDetail): string[] {
  const out = new Set<string>()
  for (const q of detail.quantizations) {
    for (const f of q.fits) {
      if (f.gpuKind === "cpu" && f.status === "no-fit" && f.reason)
        out.add(f.reason)
    }
  }
  return [...out]
}

export function HFInspect({
  onDeploy,
  deployBlocked,
}: {
  onDeploy?: (entry: EntryDetail) => void
  deployBlocked?: string
}) {
  const { can } = useOrganization()
  const inspect = useMutation(useAiModelsInspect)
  const field = useId()
  const heading = useId()
  const [value, setValue] = useState("")
  const [result, setResult] = useState<EntryDetail>()
  const [refusal, setRefusal] = useState<Refusal>()
  const admin = can("ai:manage")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setResult(undefined)
    setRefusal(undefined)
    if (looksLikeToken(value)) {
      // Never sent: a refusal could quote it back.
      setValue("")
      setRefusal({
        message:
          "That looks like a Hugging Face token. Paste a model's repo here. A token goes in AI settings.",
        settings: admin ? "link" : undefined,
      })
      return
    }
    const repo = repoOf(value)
    if (!repo) {
      setRefusal({
        message:
          "Paste a Hugging Face repo as owner/name, like Qwen/Qwen2.5-7B-Instruct, or its page's address.",
      })
      return
    }
    try {
      setResult(await inspect.mutateAsync({ body: { repo } }))
    } catch (err) {
      const message = said(err, "Loomplane couldn't read that model.")
      setRefusal({
        message,
        // A gated model needs the workspace's token, which admins add.
        settings: /AI settings/.test(message)
          ? admin
            ? "link"
            : "ask"
          : undefined,
      })
    }
  }

  const warnings = result ? cpuWarnings(result) : []
  return (
    <section
      aria-labelledby={heading}
      className="grid gap-3 rounded-[14px] bg-subtle p-4"
    >
      <div className="grid gap-1">
        <h2 id={heading} className="text-sm font-semibold">
          Any Hugging Face model
        </h2>
        <p className="max-w-[72ch] text-[12.5px] leading-relaxed text-muted-foreground">
          Loomplane reads it at its current commit and works out what it needs.
          It deploys like the models above, marked untested.
        </p>
      </div>
      <form
        onSubmit={(e) => void submit(e)}
        className="flex flex-col gap-2 sm:flex-row"
      >
        <label htmlFor={field} className="sr-only">
          Hugging Face repo
        </label>
        <Input
          id={field}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="Paste a Hugging Face repo, like Qwen/Qwen2.5-7B-Instruct"
          autoComplete="off"
          spellCheck={false}
          className="h-9 min-w-0 flex-1 bg-card font-mono text-xs"
        />
        <Button
          type="submit"
          variant="outline"
          disabled={inspect.isPending || !value.trim()}
          className="h-9 bg-card"
        >
          {inspect.isPending && <Spinner />}
          Check
        </Button>
      </form>

      {refusal && (
        <Alert variant="destructive" role="alert">
          <AlertDescription className="grid gap-2">
            <span>{refusal.message}</span>
            {refusal.settings === "ask" && (
              <span className="text-muted-foreground">
                Ask a workspace admin to change it in AI settings.
              </span>
            )}
            {refusal.settings === "link" && (
              <Link
                to="/ai/settings"
                className={buttonVariants({
                  variant: "outline",
                  size: "sm",
                  className: "w-fit",
                })}
              >
                Open AI settings
              </Link>
            )}
          </AlertDescription>
        </Alert>
      )}

      {result && (
        <div className="grid gap-2">
          {result.fitsNotice && (
            <p className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
              <InfoIcon aria-hidden="true" className="size-3.5 shrink-0" />
              {result.fitsNotice}
            </p>
          )}
          {warnings.map((w) => (
            <p
              key={w}
              className="flex items-start gap-1.5 text-[12.5px] text-weld-ink"
            >
              <TriangleAlertIcon
                aria-hidden="true"
                className="mt-0.5 size-3.5 shrink-0"
              />
              {w}
            </p>
          ))}
          <ModelCard
            entry={result}
            onDeploy={onDeploy ? () => onDeploy(result) : undefined}
            deployBlocked={deployBlocked}
            className="max-w-xl"
          />
        </div>
      )}
    </section>
  )
}
