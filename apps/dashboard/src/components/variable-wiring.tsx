// Where one service reads a database or cache it needs: in one variable,
// named for what its code reads, or in separate variables for each part,
// with what each will hold shown before anything is saved.
import { SparklesIcon, TriangleAlertIcon } from "lucide-react"

import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import {
  type HandSet,
  outputLabels,
  partOutputs,
  prefixTemplate,
  templatesFor,
  type WiringChoice,
  wiringProblem,
} from "@/lib/wiring"

/** What the variables will hold, for the preview; unknown parts stay blank. */
export type WiringSample = {
  host?: string
  port?: number | string
  user?: string
  database?: string
  /** Shown for the url, password masked. */
  url?: string
}

export function VariableWiring({
  service,
  need,
  kind,
  fallback,
  value,
  detected,
  sample,
  handSet = [],
  onChange,
}: {
  /** The service that reads it. */
  service: string
  /** What it reads: the resource's name. */
  need: string
  kind: string
  /** The variable the kind names, used when nothing else is chosen. */
  fallback: string
  value: WiringChoice
  /** What the service's code reads that the choice came from. */
  detected: string[]
  sample?: WiringSample
  /** Variables set by hand with a name it reads, which win over these. */
  handSet?: HandSet[]
  onChange: (choice: WiringChoice) => void
}) {
  const ready = templatesFor(
    kind,
    value.mode === "parts" ? value.prefix : "DB_"
  )
  // Names someone chose themselves, kept as they are.
  const templates =
    value.mode === "parts" && value.template === "custom"
      ? [...ready, { id: "custom", label: "Your own names", vars: value.vars }]
      : ready
  const problem = wiringProblem(value)
  const shown = (output: string) => {
    switch (output) {
      case "url":
        return sample?.url ?? "the connection string"
      case "password":
        return "••••••"
      case "port":
        return sample?.port !== undefined ? String(sample.port) : "its port"
      default:
        return (sample?.[output as "host" | "user" | "database"] ||
          `its ${output}`) as string
    }
  }
  const rows: [string, string][] =
    value.mode === "one"
      ? [[value.key, "url"]]
      : partOutputs.flatMap((o) =>
          value.vars[o]?.trim()
            ? [[value.vars[o]!.trim(), o] as [string, string]]
            : []
        )

  return (
    <fieldset className="grid gap-2.5 rounded-lg p-3 ring-1 ring-border">
      <legend className="sr-only">
        How {service} reads {need}
      </legend>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[13px]">
          <span className="font-mono font-medium">{service}</span> reads{" "}
          <span className="font-mono">{need}</span> in
        </p>
        <div
          role="radiogroup"
          aria-label={`How ${service} reads ${need}`}
          className="flex gap-0.5 rounded-[10px] bg-muted p-[3px]"
        >
          {[
            { mode: "one" as const, label: "One variable" },
            { mode: "parts" as const, label: "Separate variables" },
          ].map((o) => (
            <button
              key={o.mode}
              type="button"
              role="radio"
              aria-checked={value.mode === o.mode}
              onClick={() => {
                if (o.mode === value.mode) return
                onChange(
                  o.mode === "one"
                    ? { mode: "one", key: fallback }
                    : {
                        mode: "parts",
                        template: templates[0].id,
                        prefix: "DB_",
                        vars: { ...templates[0].vars },
                      }
                )
              }}
              className={cn(
                "h-7 rounded-lg px-2.5 text-[12.5px] font-medium",
                value.mode === o.mode
                  ? "bg-background text-foreground shadow-xs"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {o.label}
            </button>
          ))}
        </div>
      </div>

      {detected.length > 0 && (
        <p className="flex items-start gap-1.5 text-[12px] text-muted-foreground">
          <SparklesIcon
            className="mt-0.5 size-3.5 shrink-0"
            aria-hidden="true"
          />
          {service}&apos;s code reads {detected.join(", ")}.
        </p>
      )}

      {value.mode === "one" ? (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            aria-label={`Variable ${service} reads ${need} in`}
            className="h-8 w-60 font-mono text-[12.5px]"
            value={value.key}
            spellCheck={false}
            onChange={(e) => onChange({ mode: "one", key: e.target.value })}
          />
          {value.key !== fallback && (
            <button
              type="button"
              className="text-[12px] text-muted-foreground underline underline-offset-4 hover:text-foreground"
              onClick={() => onChange({ mode: "one", key: fallback })}
            >
              Use {fallback}
            </button>
          )}
        </div>
      ) : (
        <div className="grid gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <Select
              items={templates.map((t) => ({ label: t.label, value: t.id }))}
              value={value.template}
              onValueChange={(id) => {
                const t = templates.find((x) => x.id === id)
                if (t)
                  onChange({ ...value, template: t.id, vars: { ...t.vars } })
              }}
            >
              <SelectTrigger
                size="sm"
                aria-label="Variable names"
                className="w-56"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {templates.map((t) => (
                  <SelectItem key={t.id} value={t.id}>
                    {t.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {value.template === "prefix" && (
              <Input
                aria-label="Prefix"
                className="h-8 w-24 font-mono text-[12.5px]"
                value={value.prefix}
                onChange={(e) => {
                  const prefix = e.target.value.toUpperCase()
                  onChange({
                    ...value,
                    prefix,
                    vars: { ...prefixTemplate(prefix).vars },
                  })
                }}
              />
            )}
          </div>
          <div className="grid grid-cols-[90px_minmax(0,1fr)] items-center gap-x-3 gap-y-1.5">
            {partOutputs.map((o) => (
              <label key={o} className="contents">
                <span className="text-[12.5px] text-muted-foreground">
                  {outputLabels[o]}
                </span>
                <Input
                  aria-label={`${outputLabels[o]} variable`}
                  className="h-7 max-w-60 font-mono text-[12.5px]"
                  value={value.vars[o] ?? ""}
                  placeholder="Not read"
                  spellCheck={false}
                  onChange={(e) =>
                    onChange({
                      ...value,
                      vars: { ...value.vars, [o]: e.target.value },
                    })
                  }
                />
              </label>
            ))}
          </div>
        </div>
      )}

      {!problem && handSet.length > 0 && (
        <div
          role="status"
          className="flex items-start gap-1.5 text-[12px] text-muted-foreground"
        >
          <TriangleAlertIcon
            className="mt-0.5 size-3.5 shrink-0"
            aria-hidden="true"
          />
          <p>
            {handSet
              .map(
                (h) =>
                  `${h.key} is set by hand ${h.environment ? `in ${h.environment}` : "in every environment"}`
              )
              .join("; ")}
            . A value set by hand wins over this one, so remove it on the
            Variables tab, or choose another name.
          </p>
        </div>
      )}
      {problem ? (
        <p className="text-[12.5px] text-destructive">{problem}</p>
      ) : (
        <ul
          aria-label={`What ${service} gets`}
          className="grid gap-0.5 rounded-md bg-muted/40 px-2.5 py-2 font-mono text-[12px]"
        >
          {rows.map(([key, output]) => (
            <li key={key} className="truncate">
              {key}=
              <span className="text-muted-foreground">{shown(output)}</span>
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  )
}
