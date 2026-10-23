import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"
// Tags on a Cloud target. A service asks for tags with cloud_tags in its
// manifest and lands on the one granted target that carries all of them.
import { XIcon } from "lucide-react"
import { useState, type ReactNode } from "react"

import { cn } from "@/lib/utils"

// The API's rules: a lowercase slug of up to 32 characters, 16 per target.
// A service selects a target with at most 8.
export const maxTags = 16
export const maxServiceTags = 8
const maxTag = 32
const slug = /^[a-z][a-z0-9]*(-[a-z0-9]+)*$/

/** asTag turns what someone typed into the slug the API would keep. */
function asTag(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
}

/** tagProblem says why a tag can't be added, or nothing when it can. */
export function tagProblem(
  tag: string,
  tags: string[],
  limit = maxTags,
  owner: "target" | "service" = "target"
): string {
  if (!tag) return ""
  if (tag.length > maxTag) return `Keep a tag to ${maxTag} characters.`
  if (!slug.test(tag))
    return "Use lowercase letters, numbers and dashes, starting with a letter."
  if (tags.includes(tag))
    return owner === "target"
      ? `${tag} is already on this target.`
      : `${tag} is already selected.`
  if (tags.length >= limit)
    return owner === "target"
      ? `A target carries at most ${limit} tags.`
      : `A service selects with at most ${limit} tags.`
  return ""
}

export function TagInput({
  tags,
  onChange,
  suggestions = [],
  disabled = false,
  label = "Target tags",
  owner = "target",
  help,
}: {
  tags: string[]
  onChange: (tags: string[]) => void
  /** Tags already used on this Cloud's other targets. */
  suggestions?: string[]
  disabled?: boolean
  label?: string
  /** A target carries tags; a service selects a target with them. */
  owner?: "target" | "service"
  /** Replaces the hint shown under the field when nothing else is. */
  help?: ReactNode
}) {
  const [draft, setDraft] = useState("")
  const limit = owner === "target" ? maxTags : maxServiceTags
  const problemOf = (t: string, current: string[]) =>
    tagProblem(t, current, limit, owner)
  const tag = asTag(draft)
  const problem = problemOf(tag, tags)
  const add = (value: string) => {
    const next = asTag(value)
    if (!next || problemOf(next, tags)) return false
    onChange([...tags, next].sort())
    setDraft("")
    return true
  }
  const offered = suggestions.filter((s) => !tags.includes(s)).slice(0, 8)
  return (
    <div className="grid gap-1.5">
      <div
        className={cn(
          "flex min-h-9 flex-wrap items-center gap-1.5 rounded-lg border bg-transparent px-1.5 py-1 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/40",
          problem && draft && "border-destructive/60",
          disabled && "opacity-60"
        )}
      >
        {tags.map((t) => (
          <Badge
            variant="secondary"
            key={t}
            className="inline-flex h-6 items-center gap-1 rounded-md bg-brand-tint pr-1 pl-2 font-mono text-[11.5px] text-brand-ink"
          >
            {t}
            {!disabled && (
              <Button
                variant="ghost"
                type="button"
                aria-label={`Remove tag ${t}`}
                onClick={() => onChange(tags.filter((x) => x !== t))}
                className="flex size-4 items-center justify-center rounded-sm hover:bg-brand-ink/10"
              >
                <XIcon className="size-3" />
              </Button>
            )}
          </Badge>
        ))}
        <Input
          aria-label={label}
          aria-invalid={!!problem && !!draft}
          value={draft}
          disabled={disabled}
          placeholder={tags.length ? "" : "eu, gpu, frontend"}
          onChange={(e) => {
            const value = e.target.value
            // A comma ends a tag, so pasting "eu, gpu" adds both.
            if (value.includes(",")) {
              const parts = value.split(",")
              const rest = parts.pop() ?? ""
              let next = tags
              for (const p of parts) {
                const t = asTag(p)
                if (t && !problemOf(t, next)) next = [...next, t]
              }
              if (next !== tags) onChange([...next].sort())
              setDraft(rest)
              return
            }
            setDraft(value)
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === "Tab") {
              if (draft.trim() && add(draft)) e.preventDefault()
              else if (e.key === "Enter") e.preventDefault()
            } else if (e.key === "Backspace" && !draft && tags.length) {
              onChange(tags.slice(0, -1))
            }
          }}
          onBlur={() => {
            if (draft.trim()) add(draft)
          }}
          className="h-6 min-w-24 flex-1 rounded-none border-0 bg-transparent px-1 font-mono text-[12.5px] shadow-none outline-none placeholder:font-sans placeholder:text-muted-foreground focus-visible:border-0 focus-visible:ring-0 aria-invalid:ring-0 md:text-[12.5px] dark:bg-transparent"
        />
      </div>
      {draft && problem ? (
        <Alert variant="destructive" className="text-xs text-destructive">
          <AlertDescription>{problem}</AlertDescription>
        </Alert>
      ) : offered.length > 0 && !disabled ? (
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          {owner === "target"
            ? "Used in this Cloud:"
            : "On this Cloud's targets:"}
          {offered.map((s) => (
            <Button
              variant="ghost"
              key={s}
              type="button"
              onClick={() => add(s)}
              className="rounded-md border border-dashed px-1.5 py-px font-mono text-[11.5px] hover:border-foreground/30 hover:text-foreground"
            >
              + {s}
            </Button>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          {help ?? (
            <>
              Services ask for tags with{" "}
              <code className="font-mono">cloud_tags</code> and run on the one
              target that has them all. Press Enter or a comma to add one.
            </>
          )}
        </p>
      )}
    </div>
  )
}
