import { Badge } from "@forge-go/dashboard-kit/components/badge"
import { Button } from "@forge-go/dashboard-kit/components/button"
import { CheckIcon, CopyIcon } from "@forge-go/dashboard-kit/icons"
import { type ReactNode, useEffect, useState } from "react"

import {
  type AppSource,
  type ConsoleIntegration,
  type Health,
  type SetupState,
  brokenCount,
  setupState,
} from "./types"

const HEALTH_LABEL: Record<Health, string> = {
  sign_in_needed: "Sign-in needed",
  app_changed: "App replaced",
  failed: "Failing",
  incomplete: "Missing permissions",
  healthy: "Healthy",
}

/** What each health means, for the legend and for tooltips. */
export const HEALTH_HELP: Record<Health, string> = {
  sign_in_needed:
    "The cloud stopped honoring the grant, so nothing deploys through it until someone signs in again.",
  app_changed:
    "It signed in with an app that has since been replaced or removed, so its grant can't be renewed. Someone has to sign in again.",
  failed:
    "Its last check failed. The message says why; it may clear on the next check.",
  incomplete:
    "The sign-in works, but the account lacks permissions a deployment needs. Its last check lists them.",
  healthy: "Its last check passed.",
}

export function healthLabel(h: Health): string {
  return HEALTH_LABEL[h] ?? h
}

const warning = "border-warning/50 bg-warning/10 text-warning-foreground"

export function HealthBadge({ health }: { health: Health }) {
  switch (health) {
    case "sign_in_needed":
    case "app_changed":
    case "failed":
      return <Badge variant="destructive">{healthLabel(health)}</Badge>
    case "incomplete":
      return (
        <Badge className={warning} variant="outline">
          {healthLabel(health)}
        </Badge>
      )
    default:
      return <Badge variant="secondary">{healthLabel(health)}</Badge>
  }
}

const SOURCE_LABEL: Record<AppSource, string> = {
  platform: "Set here",
  config: "Configuration",
  none: "Not set",
}

export function sourceLabel(source: AppSource): string {
  return SOURCE_LABEL[source] ?? source
}

export function SourceBadge({ source }: { source: AppSource }) {
  return (
    <Badge variant={source === "platform" ? "secondary" : "outline"}>
      {sourceLabel(source)}
    </Badge>
  )
}

/** A state's label, which for "not set up" depends on what still works. */
export function setupLabel(
  state: SetupState,
  kind?: ConsoleIntegration["kind"]
): string {
  switch (state) {
    case "refused":
      return "App refused"
    case "broken":
      return "Broken sign-ins"
    case "ready":
      return "Ready"
    default:
      if (kind === "cloud") {
        return "Tokens only"
      }
      return kind === "repository" ? "Public only" : "Not set up"
  }
}

/** What each setup state means, in a line, for the list and its filter. */
export function setupHelp(it: ConsoleIntegration): string {
  switch (setupState(it)) {
    case "refused":
      return "The cloud refused this app. Save it again with the right values."
    case "broken":
      return "Some connections need someone to sign in again, or are failing."
    case "ready":
      return it.kind === "cloud"
        ? "People can connect it by signing in."
        : "Organizations can connect private repositories."
    default:
      return it.kind === "cloud"
        ? "People connect it with a token or key; an app adds signing in."
        : "Only public repositories can be read until it's configured."
  }
}

export function SetupBadge({
  integration,
}: {
  integration: ConsoleIntegration
}) {
  const state = setupState(integration)
  const label = setupLabel(state, integration.kind)
  switch (state) {
    case "refused":
    case "broken":
      return <Badge variant="destructive">{label}</Badge>
    case "ready":
      return (
        <Badge
          className="bg-success/15 text-success-foreground"
          variant="secondary"
        >
          {label}
        </Badge>
      )
    default:
      return <Badge variant="outline">{label}</Badge>
  }
}

function useCopied(): [boolean, (text: string) => void] {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) {
      return
    }
    const t = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(t)
  }, [copied])
  const copy = (text: string) => {
    navigator.clipboard
      ?.writeText(text)
      .then(() => setCopied(true))
      .catch(() => undefined)
  }
  return [copied, copy]
}

/** A button that copies text and says so for a moment. */
export function CopyButton({
  text,
  label,
  children,
}: {
  text: string
  label: string
  children?: ReactNode
}) {
  const [copied, copy] = useCopied()
  return (
    <Button
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation()
        copy(text)
      }}
      size="sm"
      title={label}
      type="button"
      variant="outline"
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      {children && (copied ? "Copied" : children)}
    </Button>
  )
}

/** A value an operator copies into a provider's settings. */
export function CopyValue({ value, label }: { value: string; label: string }) {
  const [copied, copy] = useCopied()
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <code className="min-w-0 rounded bg-muted px-1.5 py-0.5 font-mono text-xs break-all">
        {value}
      </code>
      <Button
        aria-label={`Copy ${label}`}
        onClick={() => copy(value)}
        size="icon-xs"
        title={`Copy ${label}`}
        type="button"
        variant="ghost"
      >
        {copied ? <CheckIcon /> : <CopyIcon />}
      </Button>
    </span>
  )
}

/** "3 sign-ins · 1 broken", or what there is to say instead. */
export function ConnectionSummary({
  integration,
}: {
  integration: ConsoleIntegration
}) {
  const c = integration.connections
  if (!c) {
    return (
      <span className="text-sm text-muted-foreground">
        {integration.kind === "repository" ? "Not tracked here" : "Unknown"}
      </span>
    )
  }
  if (c.total === 0) {
    return <span className="text-sm text-muted-foreground">None yet</span>
  }
  const bad = brokenCount(c)
  return (
    <span className="flex flex-wrap items-center gap-1.5 text-sm">
      <span>
        {c.total} {c.total === 1 ? "sign-in" : "sign-ins"}
      </span>
      {bad > 0 && <Badge variant="destructive">{bad} broken</Badge>}
      {c.incomplete > 0 && (
        <Badge className={warning} variant="outline">
          {c.incomplete} missing permissions
        </Badge>
      )}
    </span>
  )
}
