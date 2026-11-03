// Your notification inbox: deployment results, newest first. Delivery
// preferences live on the settings page and webhooks on their own.
import { CheckIcon, RefreshCwIcon, Settings2Icon } from "lucide-react"
import { type ReactNode, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useNotificationsList,
  useNotificationsRead,
  useNotificationsRetry,
  useNotificationsWebhooksList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { NotificationsIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatDate, relative } from "@/lib/format"
import {
  emailStatus,
  noticeTone,
  ReadError,
  safeAction,
  useAuthorizationFailure,
  useNotificationRefresh,
} from "@/lib/notifications"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

function AdminReadGate({ children }: { children: ReactNode }) {
  const endpoints = useQuery(useNotificationsWebhooksList)
  const denied = useAuthorizationFailure(endpoints)
  return denied ? null : children
}

export function Notifications() {
  const { session, can } = useOrganization()
  return (
    <NotificationContent key={session.orgId} admin={can("member:manage")} />
  )
}

function NotificationContent({ admin }: { admin: boolean }) {
  const inbox = useQuery(useNotificationsList)
  const read = useMutation(useNotificationsRead)
  const retry = useMutation(useNotificationsRetry)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const projects = useQuery(useProjectsList)
  const now = useNow()
  const projectName = (id: string) =>
    projects.data?.projects.find((p) => p.id === id)?.name
  const unread = (inbox.data?.items ?? []).filter((n) => !n.read)
  const inboxDenied = useAuthorizationFailure(inbox)

  useNotificationRefresh(inbox.refetch)

  const markRead = async (id: string) => {
    setError("")
    try {
      await read.mutateAsync({ path: { id } })
      invalidate(useNotificationsList)
    } catch (err) {
      setError(messageOf(err, "The notification wasn't marked as read."))
    }
  }

  const markAllRead = async () => {
    setError("")
    try {
      for (const n of unread) await read.mutateAsync({ path: { id: n.id } })
    } catch (err) {
      setError(messageOf(err, "Some notifications weren't marked as read."))
    } finally {
      invalidate(useNotificationsList)
    }
  }

  const retryProcessing = async () => {
    setError("")
    setNotice("")
    try {
      await retry.mutateAsync({})
      setNotice("Failed notification processing is queued for this workspace.")
      invalidate(useNotificationsList)
    } catch (err) {
      setError(messageOf(err, "Notification processing couldn't be retried."))
    }
  }

  return (
    <>
      <SiteHeader crumbs={[{ label: "Notifications" }]} />
      <Page className="max-w-[980px]">
        <PageHeading
          title="Notifications"
          description="Deployment results for you, newest first."
          actions={
            <>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void inbox.refetch().catch(() => undefined)}
              >
                <RefreshCwIcon /> Refresh inbox
              </Button>
              <Button
                variant="outline"
                size="sm"
                nativeButton={false}
                render={<Link to="/notifications/settings" />}
              >
                <Settings2Icon /> Settings
              </Button>
            </>
          }
        />
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {notice && (
          <p role="status" className="text-[13px] text-muted-foreground">
            {notice}
          </p>
        )}
        <section
          aria-label="Notification inbox"
          className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border"
        >
          <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
            <div className="grid gap-0.5">
              <h2 className="flex items-center gap-2 text-[14px] font-semibold">
                Your inbox
                {unread.length > 0 && (
                  <span className="rounded-full bg-brand-tint px-2 py-px text-[11.5px] font-medium text-brand-ink">
                    {unread.length} unread
                  </span>
                )}
              </h2>
              <span className="text-[12.5px] text-muted-foreground">
                Recent updates refresh every 10 seconds while this page is
                visible.
              </span>
            </div>
            <div className="flex flex-wrap gap-1">
              {admin && !inboxDenied && (
                <AdminReadGate>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={retry.isPending}
                    title="Retry exhausted email and event processing for this workspace. Webhook delivery retries are handled by Relay."
                    onClick={() => void retryProcessing()}
                  >
                    {retry.isPending && <Spinner />}Retry notification
                    processing
                  </Button>
                </AdminReadGate>
              )}
              {unread.length > 1 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-brand-ink"
                  disabled={read.isPending}
                  onClick={() => void markAllRead()}
                >
                  Mark all as read
                </Button>
              )}
            </div>
          </div>
          {inbox.status === "error" && (
            <div className="p-4">
              <ReadError
                error={inbox.error}
                fallback="Your inbox couldn't be loaded."
                retry={() => void inbox.refetch().catch(() => undefined)}
                label="Retry inbox"
              />
            </div>
          )}
          {!inbox.data && inbox.status !== "error" && (
            <div className="p-4">
              <Skeleton className="h-28 w-full" />
            </div>
          )}
          {inbox.data &&
            inbox.data.items.length === 0 &&
            inbox.status !== "error" && (
              <ZeroState surface={false} className="py-8">
                <ZeroStateHeader>
                  <ZeroStateMedia variant="icon">
                    <NotificationsIcon />
                  </ZeroStateMedia>
                  <ZeroStateTitle>No notifications yet</ZeroStateTitle>
                  <ZeroStateDescription>
                    Your deployment results appear here after a deployment
                    finishes.
                  </ZeroStateDescription>
                </ZeroStateHeader>
              </ZeroState>
            )}
          {inbox.data && !inboxDenied && inbox.data.items.length > 0 && (
            <ul className="divide-y">
              {inbox.data.items.map((item) => {
                const project = projectName(item.projectId)
                const email = emailStatus(item.emailStatus)
                return (
                  <li
                    key={item.id}
                    title={item.body}
                    className={cn(
                      "flex flex-wrap items-center gap-x-3.5 gap-y-2 px-4 py-3",
                      !item.read && "bg-brand-soft"
                    )}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "size-2 shrink-0 rounded-full",
                        noticeTone[item.status] ?? "bg-muted-foreground/50",
                        item.read && "opacity-40"
                      )}
                    />
                    <span className="grid min-w-0 flex-[1_1_320px] gap-0.5">
                      <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-[13.5px]">
                        <span
                          className={cn(
                            "font-medium",
                            item.read && "text-muted-foreground"
                          )}
                        >
                          {item.title}
                        </span>
                        {project && (
                          <span className="truncate text-muted-foreground">
                            · {project}
                          </span>
                        )}
                        {!item.read && <span className="sr-only">Unread</span>}
                      </span>
                      <span className="flex flex-wrap gap-x-2.5 gap-y-0.5 text-xs text-muted-foreground">
                        <time
                          dateTime={item.createdAt}
                          title={formatDate(item.createdAt)}
                        >
                          {relative(Date.parse(item.createdAt), now)}
                        </time>
                        {email && (
                          <span
                            className={cn(
                              item.emailStatus === "failed" &&
                                "text-destructive"
                            )}
                          >
                            {email}
                          </span>
                        )}
                      </span>
                    </span>
                    {safeAction(item.actionUrl) && (
                      <a
                        href={item.actionUrl}
                        className="text-[12.5px] font-medium text-brand-ink hover:underline"
                      >
                        View deployment
                      </a>
                    )}
                    {!item.read && (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={read.isPending}
                        onClick={() => void markRead(item.id)}
                      >
                        <CheckIcon />
                        Mark as read
                      </Button>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </section>
      </Page>
    </>
  )
}
