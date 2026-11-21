import { BoxIcon, PackageIcon } from "lucide-react"
import { Checkbox } from "@/components/ui/checkbox"
import type { Entry } from "@/lib/add-service"
import { cn } from "@/lib/utils"

/** Wiring picks which manifest entries a new service connects to. */
export function Wiring({
  label,
  help,
  options,
  value,
  onChange,
  hint,
}: {
  label: string
  help: string
  options: Entry[]
  value: string[]
  onChange: (value: string[]) => void
  hint: (e: Entry) => string
}) {
  if (options.length === 0) return null
  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1 text-[13px] leading-none font-medium">
        {label}
      </legend>
      <p className="text-xs text-muted-foreground">{help}</p>
      <div className="flex flex-wrap gap-2">
        {options.map((e) => {
          const on = value.includes(e.name)
          const Icon = e.section === "services" ? BoxIcon : PackageIcon
          return (
            <label
              key={`${e.section}:${e.name}`}
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-lg border px-2.5 py-1.5 text-[13px]",
                on ? "border-brand-line bg-brand-soft" : "bg-card"
              )}
            >
              <Checkbox
                checked={on}
                onCheckedChange={(checked) =>
                  onChange(
                    checked
                      ? [...value, e.name]
                      : value.filter((n) => n !== e.name)
                  )
                }
              />
              <Icon className="size-3.5 text-muted-foreground" />
              {e.name}
              {on && (
                <span className="font-mono text-[11px] text-muted-foreground">
                  {hint(e)}
                </span>
              )}
            </label>
          )
        })}
      </div>
    </fieldset>
  )
}
