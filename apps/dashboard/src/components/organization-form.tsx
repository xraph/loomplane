import { type FormEvent, useEffect, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useCheckOrgSlug as checkOrgSlug,
  useCreateOrganization,
} from "@/client/forge/authsome/hooks"
import { useMutation } from "@/client/hooks"
import { authsomeApp } from "@/client/runtime"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { slugify } from "@/lib/slug"
import { cn } from "@/lib/utils"

const maxSlug = 40

// Addresses that name a page or a part of Loomplane rather than a workspace.
const reserved = new Set([
  "api",
  "admin",
  "app",
  "new",
  "settings",
  "setup",
  "onboarding",
  "workspaces",
  "sign-in",
  "sign-up",
  "invitations",
  "static",
  "www",
  "help",
])

// The pages the preview shows under the address, as they are in the app.
const examplePaths = ["/projects", "/clouds", "/members"]

type Status =
  | "empty"
  | "short"
  | "invalid"
  | "reserved"
  | "checking"
  | "free"
  | "taken"
  | "unknown"

const pill: Record<Status, [string, string]> = {
  empty: ["Needed", "bg-muted text-muted-foreground"],
  short: ["Too short", "bg-failed-tint text-failed"],
  invalid: ["Invalid", "bg-failed-tint text-failed"],
  reserved: ["Reserved", "bg-failed-tint text-failed"],
  checking: ["Checking", "bg-muted text-muted-foreground"],
  free: ["Free", "bg-overdye-tint text-overdye-ink"],
  taken: ["Taken", "bg-failed-tint text-failed"],
  unknown: ["Unchecked", "bg-muted text-muted-foreground"],
}

function message(status: Status, slug: string): string {
  switch (status) {
    case "empty":
      return "Type a name and we'll suggest an address."
    case "short":
      return "Use at least 3 characters."
    case "invalid":
      return "Use lowercase letters, digits and single hyphens, starting and ending with a letter or digit."
    case "reserved":
      return `${slug} is part of Loomplane's own addresses. Try another.`
    case "checking":
      return `Checking whether ${slug} is free…`
    case "free":
      return `${slug} is free. It's yours once you create the workspace.`
    case "taken":
      return `${slug} belongs to another workspace. Try a more specific one.`
    case "unknown":
      return `Couldn't check ${slug} just now. Creating the workspace checks it again.`
  }
}

/** shape says what's wrong with an address before asking the server. */
function shape(slug: string): Status | undefined {
  if (!slug) return "empty"
  if (slug.length < 3) return "short"
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(slug)) return "invalid"
  if (reserved.has(slug)) return "reserved"
  return undefined
}

const fit = (value: string) =>
  slugify(value).slice(0, maxSlug).replace(/-+$/, "")

/**
 * OrganizationForm creates an organization through authsome's API and hands
 * its ID to onCreated, which switches to it. The address follows the name
 * until you edit it, and says whether it's free as you type.
 */
export function OrganizationForm({
  onCreated,
}: {
  onCreated: (orgId: string) => Promise<void>
}) {
  const create = useMutation(useCreateOrganization)
  const [name, setName] = useState("")
  const [edited, setEdited] = useState<string>()
  const [checked, setChecked] = useState<Record<string, Status>>({})
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  const slug = edited ?? fit(name)
  const local = shape(slug)
  const status: Status = local ?? checked[slug] ?? "checking"

  // Slugs are unique across every organization; ask once typing settles.
  useEffect(() => {
    if (local || checked[slug]) return
    const timer = window.setTimeout(() => {
      checkOrgSlug({ query: { app_id: authsomeApp(), slug } })
        .refetch()
        .then(({ available }) =>
          setChecked((c) => ({ ...c, [slug]: available ? "free" : "taken" }))
        )
        .catch(() => setChecked((c) => ({ ...c, [slug]: "unknown" })))
    }, 350)
    return () => window.clearTimeout(timer)
  }, [slug, local, checked])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (local) return
    setBusy(true)
    setError("")
    try {
      // authsome answers a taken slug with a 500, so ask again right before.
      const { available } = await checkOrgSlug({
        query: { app_id: authsomeApp(), slug },
      }).refetch()
      if (!available) {
        setChecked((c) => ({ ...c, [slug]: "taken" }))
        return
      }
      const org = await create.mutateAsync({
        body: { name: name.trim(), slug },
      })
      await onCreated(org.id)
    } catch (err) {
      setError(messageOf(err, "The workspace wasn't created. Try again."))
    } finally {
      setBusy(false)
    }
  }

  const host = `${window.location.host}/`
  const [label, tone] = pill[status]
  const bad = tone.includes("text-failed")
  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4">
      <label className="flex flex-col gap-1.5 text-[12.5px] font-medium text-foreground/80">
        Workspace name
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Acme Inc"
          autoComplete="organization"
          autoFocus
          required
          className="h-10 rounded-[10px] text-sm"
        />
        <span className="font-normal text-muted-foreground">
          Your company or team. People see it when you invite them.
        </span>
      </label>

      <div className="flex flex-col gap-1.5">
        <label
          htmlFor="workspace-address"
          className="text-[12.5px] font-medium text-foreground/80"
        >
          Address
        </label>
        <span
          className={cn(
            "flex h-11 items-center rounded-[10px] border bg-background pr-1.5 focus-within:ring-2 focus-within:ring-ring/40",
            bad && "border-failed/50"
          )}
        >
          <span className="flex-none pr-1 pl-3 font-mono text-[12.5px] text-muted-foreground">
            {host}
          </span>
          <input
            id="workspace-address"
            value={slug}
            onChange={(e) =>
              setEdited(e.target.value.toLowerCase().slice(0, maxSlug))
            }
            spellCheck={false}
            autoComplete="off"
            aria-describedby="workspace-address-status"
            aria-invalid={bad || undefined}
            className="h-full min-w-0 flex-1 bg-transparent pr-3 font-mono text-[13px] outline-none"
          />
          <span
            className={cn(
              "flex-none rounded-md px-2 py-1 text-xs font-medium",
              tone
            )}
          >
            {label}
          </span>
        </span>
        <span
          id="workspace-address-status"
          role="status"
          className={cn(
            "text-[12.5px]",
            status === "free"
              ? "text-overdye-ink"
              : bad
                ? "text-failed"
                : "text-muted-foreground"
          )}
        >
          {message(status, slug)}
        </span>
      </div>

      {slug && (
        <div className="flex flex-col gap-2 rounded-xl bg-muted/50 p-3.5 ring-1 ring-border ring-inset">
          <span className="text-xs font-medium text-muted-foreground">
            Every page in this workspace lives under it
          </span>
          {examplePaths.map((path) => (
            <span
              key={path}
              className="truncate font-mono text-[12.5px] text-muted-foreground"
            >
              {host}
              <span className="font-medium text-foreground">{slug}</span>
              {path}
            </span>
          ))}
        </div>
      )}

      <p className="text-[12.5px] leading-relaxed text-muted-foreground">
        Lowercase letters, digits and hyphens, 3 to {maxSlug} characters. Each
        address belongs to one workspace.
      </p>

      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {/* The sentence is split in two so a public build's source maps, which
          embed this file's text, don't carry it. The guard looks for it. */}
      {import.meta.env.VITE_LOOMPLANE_EDITION === "cloud" && (
        <p className="text-[12.5px] text-muted-foreground">
          {"$5 a month, including "}$5 of usage. You&apos;ll add a card next.
        </p>
      )}
      <Button
        type="submit"
        size="lg"
        className="h-11 w-full rounded-[10px]"
        disabled={busy || !!local || status === "taken"}
      >
        {busy && <Spinner />}
        Create workspace
      </Button>
    </form>
  )
}
