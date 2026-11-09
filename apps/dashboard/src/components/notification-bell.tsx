// The bell in the top bar: your latest notifications in a popover, with the
// full inbox and its settings a click away. Opening a notice marks it read.
import { BellIcon, Settings2Icon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useNotificationsList,
  useNotificationsRead,
} from "@/client/forge/loomplane/hooks"
import type { Notification } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Skeleton } from "@/components/ui/skeleton"
import { formatDate, relative } from "@/lib/format"
import {
  noticeTone,
  safeAction,
  useAuthorizationFailure,
  useNotificationRefresh,
} from "@/lib/notifications"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** How many notices the popover shows before pointing at the full inbox. */
const recentLimit = 6

export function NotificationBell() {
  const { session } = useOrganization()
  // A new workspace starts with a closed popover and its own inbox.
  return <Bell key={session.orgId} />
}

function Bell() {
  const inbox = useQuery(useNotificationsList)
  const read = useMutation(useNotificationsRead)
  const invalidate = useInvalidate()
  const navigate = useNavigate()
  const now = useNow()
  const [open, setOpen] = useState(false)
  const denied = useAuthorizationFailure(inbox)
  // Every page carries the bell, so it polls less often than the inbox.
  useNotificationRefresh(inbox.refetch, 30_000)

  const items = denied ? [] : (inbox.data?.items ?? [])
  const unread = items.filter((n) => !n.read)
  const recent = items.slice(0, recentLimit)

  const markRead = async (ids: string[]) => {
    try {
      for (const id of ids) await read.mutateAsync({ path: { id } })
    } catch (err) {
      toast.error(messageOf(err, "The notification wasn't marked as read."))
    } finally {
      invalidate(useNotificationsList)
    }
  }

  const openNotice = (item: Notification) => {
    if (!item.read) void markRead([item.id])
    setOpen(false)
    if (!safeAction(item.actionUrl)) return
    const url = new URL(item.actionUrl, window.location.origin)
    if (url.origin === window.location.origin) {
      void navigate({ href: url.pathname + url.search + url.hash })
    } else {
      window.location.assign(url.href)
    }
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="relative"
            aria-label={
              unread.length > 0
                ? `Notifications, ${unread.length} unread`
                : "Notifications"
            }
          />
        }
      >
        <BellIcon />
        {unread.length > 0 && (
          <span
            aria-hidden="true"
            className="absolute top-1 right-1 size-2 rounded-full bg-brand ring-2 ring-background"
          />
        )}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[22rem] gap-0 p-0">
        <div className="flex items-center justify-between gap-2 border-b px-3 py-2.5">
          <span className="flex items-center gap-2 text-[13.5px] font-semibold">
            Notifications
            {unread.length > 0 && (
              <span className="rounded-full bg-brand-tint px-2 py-px text-[11px] font-medium text-brand-ink tabular-nums">
                {unread.length} unread
              </span>
            )}
          </span>
          {unread.length > 0 && (
            <Button
              variant="ghost"
              size="xs"
              className="text-brand-ink"
              disabled={read.isPending}
              onClick={() => void markRead(unread.map((n) => n.id))}
            >
              Mark all read
            </Button>
          )}
        </div>
        <div className="max-h-[22rem] overflow-y-auto">
          {inbox.status === "error" || denied ? (
            <div className="grid justify-items-start gap-2 px-3 py-4 text-[12.5px] text-muted-foreground">
              Your notifications couldn't be loaded.
              <Button
                variant="outline"
                size="xs"
                onClick={() => void inbox.refetch().catch(() => undefined)}
              >
                Try again
              </Button>
            </div>
          ) : !inbox.data ? (
            <div className="grid gap-2 p-3">
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
            </div>
          ) : recent.length === 0 ? (
            <p className="px-3 py-6 text-center text-[12.5px] text-muted-foreground">
              No notifications yet. Deployment results show up here.
            </p>
          ) : (
            <ul className="divide-y">
              {recent.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    title={item.body}
                    onClick={() => openNotice(item)}
                    className={cn(
                      "flex w-full items-start gap-2.5 px-3 py-2.5 text-left outline-none hover:bg-muted focus-visible:bg-muted",
                      !item.read && "bg-brand-soft"
                    )}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "mt-1.5 size-2 shrink-0 rounded-full",
                        noticeTone[item.status] ?? "bg-muted-foreground/50",
                        item.read && "opacity-40"
                      )}
                    />
                    <span className="grid min-w-0 gap-0.5">
                      <span
                        className={cn(
                          "truncate text-[13px] font-medium",
                          item.read && "text-muted-foreground"
                        )}
                      >
                        {item.title}
                        {!item.read && (
                          <span className="sr-only">, unread</span>
                        )}
                      </span>
                      <time
                        dateTime={item.createdAt}
                        title={formatDate(item.createdAt)}
                        className="text-xs text-muted-foreground"
                      >
                        {relative(Date.parse(item.createdAt), now)}
                      </time>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="flex items-center justify-between gap-2 border-t px-1.5 py-1.5">
          <Link
            to="/notifications"
            className={buttonVariants({ variant: "ghost", size: "sm" })}
            onClick={() => setOpen(false)}
          >
            View all notifications
          </Link>
          <Link
            to="/notifications/settings"
            aria-label="Notification settings"
            className={buttonVariants({ variant: "ghost", size: "icon-sm" })}
            onClick={() => setOpen(false)}
          >
            <Settings2Icon />
          </Link>
        </div>
      </PopoverContent>
    </Popover>
  )
}
