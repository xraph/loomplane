// The registry's filters. They live in the page's URL, so a filtered list
// can be shared and survives a reload; this only reads and writes them.
import { SearchIcon } from "lucide-react"
import { useEffect, useId, useRef, useState } from "react"

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import {
  modelSizes,
  modelTasks,
  type ModelSize,
  type ModelsSearch,
} from "@/lib/models"
import { cn } from "@/lib/utils"

const any = "any"

export type LicenseOption = { id: string; title: string }

export function ModelFilters({
  search,
  counts,
  licenses,
  onChange,
}: {
  search: ModelsSearch
  /** Entries per task, with every other filter applied. */
  counts: Record<string, number> | undefined
  licenses: LicenseOption[]
  onChange: (next: ModelsSearch) => void
}) {
  const set = (patch: Partial<ModelsSearch>) => {
    const next: ModelsSearch = { ...search, ...patch }
    for (const key of Object.keys(next) as (keyof ModelsSearch)[]) {
      if (next[key] === undefined) delete next[key]
    }
    onChange(next)
  }
  const sizes = [{ value: any, label: "Any size" }, ...modelSizes]
  const licenseItems = [
    { value: any, label: "Any license" },
    ...licenses.map((l) => ({ value: l.id, label: l.title })),
  ]
  // A license from a shared link that this list doesn't have still shows.
  if (search.license && !licenses.some((l) => l.id === search.license))
    licenseItems.push({ value: search.license, label: search.license })

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <SearchBox
          value={search.q ?? ""}
          onChange={(q) => set({ q: q.trim() ? q : undefined })}
        />
        <Select
          items={sizes}
          value={search.size ?? any}
          onValueChange={(v) =>
            set({ size: v && v !== any ? (v as ModelSize) : undefined })
          }
        >
          <SelectTrigger
            aria-label="Size"
            className="h-9 min-w-0 flex-[1_1_9rem] sm:flex-none"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sizes.map((s) => (
              <SelectItem key={s.value} value={s.value}>
                {s.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          items={licenseItems}
          value={search.license ?? any}
          onValueChange={(v) =>
            set({ license: v && v !== any ? String(v) : undefined })
          }
        >
          <SelectTrigger
            aria-label="License"
            className="h-9 min-w-0 flex-[1_1_9rem] sm:max-w-[16rem] sm:flex-none"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {licenseItems.map((l) => (
              <SelectItem key={l.value} value={l.value}>
                {l.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div
        role="group"
        aria-label="Task"
        className="flex flex-wrap items-center gap-1.5"
      >
        {modelTasks.map((t) => {
          const on = search.task === t.value
          return (
            <button
              key={t.value}
              type="button"
              aria-pressed={on}
              onClick={() => set({ task: on ? undefined : t.value })}
              className={cn(
                "flex h-[30px] items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium ring-1 transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
                on
                  ? "bg-primary text-primary-foreground ring-primary"
                  : "bg-card text-muted-foreground ring-border hover:text-foreground"
              )}
            >
              {t.label}
              <span
                className={cn(
                  "font-mono text-[11px]",
                  on ? "text-primary-foreground/80" : "text-muted-foreground"
                )}
              >
                {counts ? (counts[t.value] ?? 0) : ""}
              </span>
            </button>
          )
        })}
      </div>

      <div className="flex flex-wrap items-center gap-x-5 gap-y-2.5">
        <Toggle
          label="Commercial use only"
          checked={!!search.commercial}
          onChange={(on) => set({ commercial: on || undefined })}
        />
        <Toggle
          label="Fits my GPUs"
          checked={!!search.fits}
          onChange={(on) => set({ fits: on || undefined })}
        />
        <Toggle
          label="Tested only"
          checked={!!search.tested}
          onChange={(on) => set({ tested: on || undefined })}
        />
      </div>
    </div>
  )
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (on: boolean) => void
}) {
  const id = useId()
  return (
    <span className="flex items-center gap-2">
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
      <label htmlFor={id} className="text-[13px] text-muted-foreground">
        {label}
      </label>
    </span>
  )
}

// The search box writes to the URL a moment after typing stops, so the
// registry isn't asked once per key.
function SearchBox({
  value,
  onChange,
}: {
  value: string
  onChange: (q: string) => void
}) {
  const [text, setText] = useState(value)
  const sent = useRef(value)
  const change = useRef(onChange)
  useEffect(() => {
    change.current = onChange
  })
  // The URL changed from outside, such as Back.
  useEffect(() => {
    if (value !== sent.current) {
      sent.current = value
      setText(value)
    }
  }, [value])
  useEffect(() => {
    if (text === sent.current) return
    const id = window.setTimeout(() => {
      sent.current = text
      change.current(text)
    }, 250)
    return () => window.clearTimeout(id)
  }, [text])
  return (
    <label className="relative flex min-w-0 flex-[1_1_100%] items-center sm:flex-[1_1_260px]">
      <SearchIcon className="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
      <span className="sr-only">Search models</span>
      <input
        data-page-search
        type="search"
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="Search models"
        className="h-9 w-full rounded-[10px] border border-transparent bg-muted pr-3 pl-9 text-[13.5px] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
      />
    </label>
  )
}
