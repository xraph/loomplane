// The workspace's monthly AI budget: the most its AI can spend a month,
// shared out between the agent and each model gateway. Every share comes out
// of it, so it has to be set before any of them can have one.
import { CheckIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useAiSettingsGet,
  useAiSettingsSetBudget,
} from "@/client/forge/loomplane/hooks"
import type { View } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation } from "@/client/hooks"
import { Panel } from "@/components/panel"
import {
  FieldProblem,
  SettingsCardFooter,
  SettingsSection,
} from "@/components/settings-section"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Progress } from "@/components/ui/progress"
import { Spinner } from "@/components/ui/spinner"

/** AIBudget is the AI settings page's budget section. */
export function AIBudget({ settings }: { settings: View }) {
  const save = useMutation(useAiSettingsSetBudget)
  const invalidate = useInvalidate()
  const field = useId()
  const total = Number(settings.monthlyBudgetUsd)
  const [value, setValue] = useState(total > 0 ? settings.monthlyBudgetUsd : "")
  const [problem, setProblem] = useState("")
  const [saved, setSaved] = useState(false)
  const shares = settings.allocations.filter((a) => a !== null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setProblem("")
    setSaved(false)
    try {
      await save.mutateAsync({
        body: {
          monthlyBudgetUsd: value.trim() || "0",
          revision: settings.revision,
        },
      })
      setSaved(true)
      invalidate(useAiSettingsGet)
    } catch (err) {
      setProblem(messageOf(err, "The budget wasn't saved."))
    }
  }

  return (
    <SettingsSection
      title="AI budget"
      description="The most this workspace's AI can spend a month. The agent and each model gateway get a share of it, so set it before giving them one."
    >
      <Panel className="gap-0 p-0">
        <form onSubmit={(e) => void submit(e)} className="grid">
          <div className="grid gap-4 px-4 py-4 md:px-5">
            <dl className="grid grid-cols-3 gap-3">
              <Figure label="Budget" usd={settings.monthlyBudgetUsd} />
              <Figure label="Shared out" usd={settings.allocatedUsd} />
              <Figure label="Left" usd={settings.unallocatedUsd} />
            </dl>
            {total > 0 && (
              <Progress
                aria-label="Budget shared out"
                value={Math.min(
                  100,
                  (Number(settings.allocatedUsd) / total) * 100
                )}
              />
            )}
            {shares.length > 0 && (
              <ul className="grid gap-1.5 rounded-[10px] bg-subtle px-3.5 py-2.5">
                {shares.map((a) => (
                  <li
                    key={a.scope}
                    className="flex items-baseline justify-between gap-3 text-[13px]"
                  >
                    <span className="truncate">{a.label}</span>
                    <span className="font-mono text-[12.5px] tabular-nums">
                      ${a.monthlyBudgetUsd}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {settings.canManage && (
              <Field>
                <FieldLabel htmlFor={field}>Workspace budget (USD)</FieldLabel>
                <Input
                  id={field}
                  inputMode="decimal"
                  value={value}
                  onChange={(e) => {
                    setValue(e.target.value)
                    setSaved(false)
                  }}
                  placeholder="For example, 250"
                  aria-invalid={!!problem}
                  className="max-w-[200px] tabular-nums"
                />
                <FieldProblem text={problem} />
              </Field>
            )}
          </div>
          <SettingsCardFooter>
            {saved ? (
              <p
                role="status"
                className="flex items-center gap-1.5 text-[12.5px] text-overdye-ink"
              >
                <CheckIcon className="size-4 shrink-0" />
                Saved.
              </p>
            ) : (
              <p className="text-[12.5px] text-muted-foreground">
                {settings.canManage
                  ? "It can't go below what's shared out."
                  : "Only workspace admins can change the budget."}
              </p>
            )}
            {settings.canManage && (
              <Button type="submit" size="sm" disabled={save.isPending}>
                {save.isPending && <Spinner />}
                Save
              </Button>
            )}
          </SettingsCardFooter>
        </form>
      </Panel>
    </SettingsSection>
  )
}

function Figure({ label, usd }: { label: string; usd: string }) {
  return (
    <div className="grid min-w-0 gap-1">
      <dt className="text-[11.5px] text-muted-foreground">{label}</dt>
      <dd className="font-mono text-[18px] leading-none font-medium tracking-[-0.02em] tabular-nums">
        ${usd}
      </dd>
    </div>
  )
}
