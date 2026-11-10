import { useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  usePlatformAgentFlagsDismiss,
  usePlatformAgentFlagsList,
} from "@/client/forge/loomplane/hooks"
import type { Flag } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { PageNotFound } from "@/components/route-feedback"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { reasonLabel } from "@/lib/agent-flags"
import { formatDate } from "@/lib/format"
import { useInstance } from "@/lib/instance"

type Status = "open" | "dismissed" | "all"

const filters: { value: Status; label: string }[] = [
  { value: "open", label: "Open" },
  { value: "dismissed", label: "Dismissed" },
  { value: "all", label: "All" },
]

/** PlatformFlags is the platform owner's page for agent answers people reported. */
export function PlatformFlags() {
  const { platformOwner } = useInstance()
  if (!platformOwner) return <PageNotFound />
  return <Flags />
}

function Flags() {
  const [status, setStatus] = useState<Status>("open")
  const flags = useQuery(usePlatformAgentFlagsList, { query: { status } })
  const dismiss = useMutation(usePlatformAgentFlagsDismiss)
  const invalidate = useInvalidate()
  const [working, setWorking] = useState<string>()

  // The server hides this from anyone but the owner with a 404.
  if (statusOf(flags.error) === 404) return <PageNotFound />

  const dismissFlag = async (flag: Flag) => {
    setWorking(flag.id)
    try {
      await dismiss.mutateAsync({ path: { id: flag.id } })
      invalidate(usePlatformAgentFlagsList)
      toast.success("Report dismissed.")
    } catch (err) {
      toast.error(messageOf(err, "The report wasn't dismissed."))
    } finally {
      setWorking(undefined)
    }
  }

  return (
    <>
      <SiteHeader crumbs={[{ label: "Flagged answers" }]} />
      <Page>
        <PageHeading
          title="Flagged answers"
          description="Answers from the agent that people reported. Each report keeps the question and the answer as they were. Dismiss one once you have looked at it."
        />
        <Tabs value={status} onValueChange={(v) => setStatus(v as Status)}>
          <TabsList>
            {filters.map((f) => (
              <TabsTrigger key={f.value} value={f.value}>
                {f.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        {flags.status === "error" && (
          <QueryError
            error={flags.error}
            fallback="Flagged answers couldn't be loaded."
            retry={flags.refetch}
          />
        )}
        {!flags.data && flags.status !== "error" && (
          <Skeleton className="h-32 w-full rounded-xl" />
        )}
        {flags.data && flags.data.flags.length === 0 && (
          <p className="text-[13px] text-muted-foreground">
            {status === "open"
              ? "Nothing is waiting. Reports show up here when someone flags an answer."
              : "No reports here."}
          </p>
        )}
        {flags.data && flags.data.flags.length > 0 && (
          <ul className="grid gap-3">
            {flags.data.flags.map((f) => (
              <FlagRow
                key={f.id}
                flag={f}
                busy={working === f.id}
                onDismiss={() => void dismissFlag(f)}
              />
            ))}
          </ul>
        )}
      </Page>
    </>
  )
}

function FlagRow({
  flag,
  busy,
  onDismiss,
}: {
  flag: Flag
  busy: boolean
  onDismiss: () => void
}) {
  return (
    <li className="grid gap-3 rounded-xl bg-card p-4 text-[13px] ring-1 ring-border">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="font-semibold">{reasonLabel(flag.reason)}</span>
          <span className="font-mono text-[12px] text-muted-foreground">
            {flag.orgId}
          </span>
          <time
            dateTime={flag.createdAt}
            className="text-[12px] text-muted-foreground"
          >
            {formatDate(flag.createdAt)}
          </time>
        </div>
        {flag.dismissedAt ? (
          <span className="text-[12px] text-muted-foreground">
            Dismissed {formatDate(flag.dismissedAt)}
          </span>
        ) : (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={onDismiss}
          >
            Dismiss
          </Button>
        )}
      </div>
      {flag.note && <p className="whitespace-pre-wrap">{flag.note}</p>}
      <div className="grid gap-1">
        <p className="text-[12px] font-medium text-muted-foreground">
          Question
        </p>
        <p className="whitespace-pre-wrap">{flag.question}</p>
      </div>
      <div className="grid gap-1">
        <p className="text-[12px] font-medium text-muted-foreground">Answer</p>
        {/* Plain text: it is a stranger's report of text a model wrote, so
            no markdown and no links to follow. */}
        <p className="max-h-72 overflow-y-auto rounded-md bg-muted/50 p-3 whitespace-pre-wrap">
          {flag.answer}
        </p>
      </div>
    </li>
  )
}
