// The parts of a workspace service's check both the register dialog and
// the service's page show: where the check runs from, and what it found
// in words rather than flags.
import { CheckIcon, CircleAlertIcon, InfoIcon, XIcon } from "lucide-react"

import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import type { Capabilities, CheckResult } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { targetsFor } from "@/lib/catalog-add"
import { engineTitles, type ConnectionEngine } from "@/lib/connection-string"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

/** The servers and clusters a check can run from, by connection. */
export function useCheckTargets() {
  const connections = useQuery(useConnectionsList)
  const targets = targetsFor(
    { targets: ["server", "kubernetes"] },
    connections.data?.connections ?? []
  )
  return {
    targets,
    loading: !connections.data && connections.status !== "error",
  }
}

/**
 * CheckFrom picks the server or cluster a check runs from: the service
 * has to be reachable from where the projects that use it run.
 */
export function CheckFrom({
  id,
  value,
  onChange,
}: {
  id: string
  value: string
  onChange: (id: string) => void
}) {
  const { targets, loading } = useCheckTargets()
  if (loading) return <Skeleton className="h-8 w-60" />
  if (targets.length === 0)
    return (
      <p className="text-[12.5px] text-muted-foreground">
        A check runs from one of your servers or clusters, and you have none
        connected yet.{" "}
        <Link
          to="/integrations"
          className="text-foreground underline underline-offset-4"
        >
          Connect one
        </Link>
      </p>
    )
  return (
    <Select
      items={targets.map((t) => ({
        label: t.connection.name,
        value: t.connection.id,
      }))}
      value={value || targets[0].connection.id}
      onValueChange={(v) => onChange(v as string)}
    >
      <SelectTrigger id={id} size="sm" className="w-full sm:w-72">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {targets.map((t) => (
          <SelectItem key={t.connection.id} value={t.connection.id}>
            {t.connection.name}
            <span className="ml-1.5 text-muted-foreground">
              {t.provider === "kubernetes" ? "cluster" : "server"}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** capabilityWords say what credentials may do, one line each. */
export function capabilityWords(
  c?: Capabilities,
  who = "These credentials"
): string[] {
  if (!c) return []
  if (c.createDatabase && c.createUser)
    return [
      `${who} can create databases and users, so each service can get its own.`,
    ]
  if (c.createDatabase) return [`${who} can create databases, but not users.`]
  if (c.createUser) return [`${who} can create users, but not databases.`]
  return [
    `${who} can't create databases or users: every service shares what's here.`,
  ]
}

/**
 * CheckReport is what a check found: a line for each good thing, then
 * each problem, in the tone it deserves.
 */
export function CheckReport({
  engine,
  result,
  tls,
}: {
  engine: ConnectionEngine | string
  result: Pick<
    CheckResult,
    "ok" | "version" | "problems" | "missing" | "notes" | "capabilities"
  >
  tls?: string
}) {
  const title = engineTitles[engine as ConnectionEngine] ?? engine
  const good: string[] = []
  if (result.version) good.push(`${title} ${result.version} answered.`)
  if (tls === "verify-full")
    good.push("Reached over TLS, with its certificate verified.")
  else if (tls === "require")
    good.push("Reached over TLS, without verifying its certificate.")
  const lines: { tone: "ok" | "bad" | "note"; text: string }[] = [
    ...good.map((text) => ({ tone: "ok" as const, text })),
    ...capabilityWords(result.capabilities).map((text) => ({
      tone: "note" as const,
      text,
    })),
    ...(result.notes ?? []).map((text) => ({ tone: "note" as const, text })),
    ...(result.problems ?? []).map((text) => ({ tone: "bad" as const, text })),
    ...(result.missing ?? []).map((m) => ({
      tone: "bad" as const,
      text: `It lacks the ${m} extension.`,
    })),
  ]
  if (tls === "disable" && result.ok)
    lines.push({
      tone: "note",
      text: "Reached without TLS: only use this on a private network.",
    })
  return (
    <div
      role="status"
      className={cn(
        "grid gap-1.5 rounded-lg px-3.5 py-3 text-[13px] ring-1",
        result.ok
          ? "bg-overdye-tint/40 ring-overdye/25"
          : "bg-failed-tint/40 ring-destructive/25"
      )}
    >
      <p className="font-medium">
        {result.ok ? "It checks out." : "The check found a problem."}
      </p>
      <ul className="grid gap-1">
        {lines.map((l, i) => {
          const Icon =
            l.tone === "ok"
              ? CheckIcon
              : l.tone === "bad"
                ? XIcon
                : l.text.includes("can't")
                  ? CircleAlertIcon
                  : InfoIcon
          return (
            <li key={i} className="flex items-start gap-2 text-pretty">
              <Icon
                aria-hidden="true"
                className={cn(
                  "mt-0.5 size-3.5 shrink-0",
                  l.tone === "ok" && "text-overdye-ink",
                  l.tone === "bad" && "text-destructive",
                  l.tone === "note" && "text-muted-foreground"
                )}
              />
              <span
                className={cn(l.tone === "note" && "text-muted-foreground")}
              >
                {l.text}
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
