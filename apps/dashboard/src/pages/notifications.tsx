import {
  BellIcon,
  CheckIcon,
  CopyIcon,
  PlusIcon,
  RefreshCwIcon,
  WebhookIcon,
} from "lucide-react"
import type { UseQueryResult } from "@forge-go/client-react"
import { type FormEvent, type ReactNode, useEffect, useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useNotificationsList,
  useNotificationsPreferencesGet,
  useNotificationsPreferencesUpdate,
  useNotificationsRead,
  useNotificationsRetry,
  useNotificationsWebhooksCreate,
  useNotificationsWebhooksDelete,
  useNotificationsWebhooksDeliveries,
  useNotificationsWebhooksList,
  useNotificationsWebhooksRotate,
} from "@/client/forge/loomplane/hooks"
import type {
  WebhookEndpointList,
  WebhookEndpointSecret,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"

// Relay delivery and Herald inbox changes are read through the generated
// queries until they have bindings on the shared organization stream.
function useNotificationRefresh(refetch: () => Promise<unknown>) {
  useEffect(() => {
    const refresh = () => {
      if (!document.hidden) void refetch().catch(() => undefined)
    }
    const timer = window.setInterval(refresh, 10_000)
    window.addEventListener("focus", refresh)
    document.addEventListener("visibilitychange", refresh)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener("focus", refresh)
      document.removeEventListener("visibilitychange", refresh)
    }
  }, [refetch])
}

function authorizationFailed(error: unknown): boolean {
  return statusOf(error) === 401 || statusOf(error) === 403
}

// Keep revoked data hidden until a fresh read succeeds.
function useAuthorizationFailure(query: { status: string; error: unknown }) {
  const [denied, setDenied] = useState(false)
  const nextDenied =
    query.status === "success"
      ? false
      : denied || authorizationFailed(query.error)
  if (denied !== nextDenied) setDenied(nextDenied)
  return nextDenied
}

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
  const preferences = useQuery(useNotificationsPreferencesGet)
  const save = useMutation(useNotificationsPreferencesUpdate)
  const read = useMutation(useNotificationsRead)
  const retry = useMutation(useNotificationsRetry)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const inboxDenied = useAuthorizationFailure(inbox)
  const preferencesDenied = useAuthorizationFailure(preferences)
  const accessDenied = inboxDenied || preferencesDenied
  useNotificationRefresh(inbox.refetch)
  useNotificationRefresh(preferences.refetch)

  const saveEmail = async (deploymentEmail: boolean) => {
    setError("")
    try {
      await save.mutateAsync({ body: { deploymentEmail } })
      invalidate(useNotificationsPreferencesGet)
    } catch (err) {
      setError(messageOf(err, "Your preference wasn't saved."))
    }
  }

  const markRead = async (id: string) => {
    setError("")
    try {
      await read.mutateAsync({ path: { id } })
      invalidate(useNotificationsList)
    } catch (err) {
      setError(messageOf(err, "The notification wasn't marked as read."))
    }
  }

  const retryProcessing = async () => {
    setError("")
    setNotice("")
    try {
      await retry.mutateAsync({})
      setNotice(
        "Failed notification processing is queued for this organization."
      )
      invalidate(useNotificationsList)
    } catch (err) {
      setError(messageOf(err, "Notification processing couldn't be retried."))
    }
  }

  return (
    <>
      <SiteHeader crumbs={[{ label: "Notifications" }]} />
      <Page className="gap-5">
        <PageHeading
          title="Notifications"
          description="Deployment results for you, and webhooks for this organization."
          actions={
            <>
              {admin && !accessDenied && (
                <AdminReadGate>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={retry.isPending}
                    title="Retry exhausted email and event processing for this organization. Webhook delivery retries are handled by Relay."
                    onClick={() => void retryProcessing()}
                  >
                    {retry.isPending && <Spinner />}Retry notification
                    processing
                  </Button>
                </AdminReadGate>
              )}
              <Button
                variant="outline"
                size="sm"
                onClick={() => void inbox.refetch().catch(() => undefined)}
              >
                <RefreshCwIcon /> Refresh inbox
              </Button>
            </>
          }
        />
        <Card className="gap-3 p-4">
          {preferences.data && !preferencesDenied ? (
            <>
              <div className="flex items-center justify-between gap-4">
                <div className="grid gap-1">
                  <label
                    htmlFor="deployment-email"
                    className="text-sm font-medium"
                  >
                    Deployment email
                  </label>
                  <p className="text-xs text-muted-foreground">
                    Email results for deployments you started. Account recovery
                    is separate.
                  </p>
                </div>
                <Switch
                  id="deployment-email"
                  checked={preferences.data.deploymentEmail}
                  disabled={save.isPending}
                  onCheckedChange={(checked) => void saveEmail(checked)}
                />
              </div>
              {!preferences.data.mailEnabled && (
                <p role="status" className="text-xs text-muted-foreground">
                  Email delivery is disabled. Your preference is saved for when
                  an administrator enables a mail provider.
                </p>
              )}
            </>
          ) : preferences.status === "error" ? (
            <ReadError
              error={preferences.error}
              fallback="Your preferences couldn't be loaded."
              retry={() => void preferences.refetch().catch(() => undefined)}
              label="Retry preferences"
            />
          ) : (
            <Skeleton className="h-12 w-full" />
          )}
        </Card>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {notice && (
          <p role="status" className="text-sm text-muted-foreground">
            {notice}
          </p>
        )}
        <section aria-label="Notification inbox" className="grid gap-3">
          <SectionHeading
            title="Your inbox"
            description="Recent updates refresh every 10 seconds while this page is visible."
          />
          {inbox.status === "error" && (
            <ReadError
              error={inbox.error}
              fallback="Your inbox couldn't be loaded."
              retry={() => void inbox.refetch().catch(() => undefined)}
              label="Retry inbox"
            />
          )}
          {!inbox.data && inbox.status !== "error" && (
            <Skeleton className="h-36 w-full" />
          )}
          {inbox.data &&
            inbox.data.items.length === 0 &&
            inbox.status !== "error" && (
              <Card className="p-0">
                <ZeroState>
                  <ZeroStateHeader>
                    <ZeroStateMedia variant="icon">
                      <BellIcon />
                    </ZeroStateMedia>
                    <ZeroStateTitle>No notifications yet</ZeroStateTitle>
                    <ZeroStateDescription>
                      Your deployment results appear here after a deployment
                      finishes.
                    </ZeroStateDescription>
                  </ZeroStateHeader>
                </ZeroState>
              </Card>
            )}
          {inbox.data && !inboxDenied && inbox.data.items.length > 0 && (
            <Card className="gap-0 overflow-hidden py-0">
              <ul className="divide-y">
                {inbox.data.items.map((item) => (
                  <li
                    key={item.id}
                    className="flex flex-wrap items-start gap-3 px-4 py-3"
                  >
                    <BellIcon className="mt-1 size-4 shrink-0 text-muted-foreground" />
                    <div className="grid min-w-0 flex-1 gap-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">
                          {item.title}
                        </span>
                        {!item.read && (
                          <Badge variant="secondary">Unread</Badge>
                        )}
                        {emailStatus(item.emailStatus) && (
                          <Badge
                            variant={
                              item.emailStatus === "failed"
                                ? "destructive"
                                : "outline"
                            }
                          >
                            {emailStatus(item.emailStatus)}
                          </Badge>
                        )}
                      </div>
                      <p className="text-sm break-words text-muted-foreground">
                        {item.body}
                      </p>
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                        <time dateTime={item.createdAt}>
                          {formatDate(item.createdAt)}
                        </time>
                        {safeAction(item.actionUrl) && (
                          <a
                            href={item.actionUrl}
                            className="underline underline-offset-4"
                          >
                            View deployment
                          </a>
                        )}
                      </div>
                    </div>
                    {!item.read && (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={read.isPending}
                        onClick={() => void markRead(item.id)}
                      >
                        <CheckIcon />
                        Mark as read
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </section>
        {admin && !accessDenied && <Webhooks />}
      </Page>
    </>
  )
}

function safeAction(value: string): boolean {
  if (!value) return false
  try {
    const url = new URL(value, window.location.origin)
    return (
      (url.protocol === "https:" || url.protocol === "http:") &&
      !url.username &&
      !url.password
    )
  } catch {
    return false
  }
}

function ReadError({
  error,
  fallback,
  retry,
  label,
}: {
  error: unknown
  fallback: string
  retry: () => void
  label: string
}) {
  return (
    <Alert
      variant="destructive"
      className="flex flex-wrap items-center justify-between gap-3"
    >
      <AlertDescription>{messageOf(error, fallback)}</AlertDescription>
      <Button variant="outline" size="sm" onClick={retry}>
        {label}
      </Button>
    </Alert>
  )
}

function Webhooks() {
  const endpoints = useQuery(useNotificationsWebhooksList)
  const denied = useAuthorizationFailure(endpoints)
  useNotificationRefresh(endpoints.refetch)
  // Remount on lost access so open dialogs and one-time secrets are discarded.
  return (
    <WebhookContent
      key={denied ? "denied" : "authorized"}
      endpoints={endpoints}
      denied={denied}
    />
  )
}

function WebhookContent({
  endpoints,
  denied,
}: {
  endpoints: UseQueryResult<WebhookEndpointList>
  denied: boolean
}) {
  const rotate = useMutation(useNotificationsWebhooksRotate)
  const remove = useMutation(useNotificationsWebhooksDelete)
  const invalidate = useInvalidate()
  const [creating, setCreating] = useState(false)
  const [rotating, setRotating] = useState<string>()
  const [secret, setSecret] = useState("")
  const [history, setHistory] = useState<{ id: string; url: string }>()
  const [error, setError] = useState("")

  const rotateSecret = async (id: string) => {
    setError("")
    try {
      const result = (await rotate.mutateAsync({
        path: { id },
      })) as WebhookEndpointSecret
      setSecret(result.secret)
      invalidate(useNotificationsWebhooksList)
    } catch (err) {
      setError(messageOf(err, "The signing secret wasn't rotated."))
    } finally {
      rotate.reset()
    }
  }

  const deleteEndpoint = async (id: string) => {
    setError("")
    try {
      await remove.mutateAsync({ path: { id } })
      invalidate(useNotificationsWebhooksList)
    } catch (err) {
      setError(messageOf(err, "The webhook wasn't deleted."))
    }
  }

  return (
    <section aria-label="Organization webhooks" className="grid gap-3">
      <SectionHeading
        title="Organization webhooks"
        description="Relay signs deployment events and retries delivery failures."
        action={
          !denied && (
            <Button size="sm" onClick={() => setCreating(true)}>
              <PlusIcon />
              Add webhook
            </Button>
          )
        }
      />
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {endpoints.status === "error" && (
        <ReadError
          error={endpoints.error}
          fallback="Webhooks couldn't be loaded."
          retry={() => void endpoints.refetch().catch(() => undefined)}
          label="Retry webhooks"
        />
      )}
      {!endpoints.data && endpoints.status !== "error" && (
        <Skeleton className="h-28 w-full" />
      )}
      {endpoints.data &&
        endpoints.data.items.length === 0 &&
        endpoints.status !== "error" && (
          <Card className="p-0">
            <ZeroState>
              <ZeroStateHeader>
                <ZeroStateMedia variant="icon">
                  <WebhookIcon />
                </ZeroStateMedia>
                <ZeroStateTitle>No webhooks configured</ZeroStateTitle>
                <ZeroStateDescription>
                  Add a public HTTPS endpoint to receive deployment results for
                  this organization.
                </ZeroStateDescription>
              </ZeroStateHeader>
            </ZeroState>
          </Card>
        )}
      {endpoints.data && !denied && endpoints.data.items.length > 0 && (
        <Card className="gap-0 py-0">
          <ul className="divide-y">
            {endpoints.data.items.map((endpoint) => (
              <li
                key={endpoint.id}
                className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
              >
                <div className="grid min-w-0 flex-1 basis-48 gap-1">
                  <span className="text-sm font-medium break-all">
                    {endpoint.url}
                  </span>
                  <span className="text-xs break-words text-muted-foreground">
                    {endpoint.description || "Deployment events"}
                  </span>
                  {!endpoint.enabled && (
                    <Badge variant="outline">Disabled by delivery policy</Badge>
                  )}
                </div>
                <div className="flex flex-wrap gap-1">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setHistory({ id: endpoint.id, url: endpoint.url })
                    }
                  >
                    Delivery history
                  </Button>
                  <AlertDialog
                    open={rotating === endpoint.id}
                    onOpenChange={(open) =>
                      setRotating(open ? endpoint.id : undefined)
                    }
                  >
                    <AlertDialogTrigger
                      render={
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={rotate.isPending}
                        >
                          Rotate secret
                        </Button>
                      }
                    />
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>
                          Rotate webhook signing secret?
                        </AlertDialogTitle>
                        <AlertDialogDescription>
                          Your receiver must use the new secret after rotation.
                          The current secret stops verifying new deliveries.
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction
                          onClick={() => {
                            setRotating(undefined)
                            void rotateSecret(endpoint.id)
                          }}
                        >
                          Rotate secret
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                  <AlertDialog>
                    <AlertDialogTrigger
                      render={
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={remove.isPending}
                        >
                          Delete
                        </Button>
                      }
                    />
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>Delete webhook?</AlertDialogTitle>
                        <AlertDialogDescription>
                          New deployment events will no longer be sent to{" "}
                          {endpoint.url}.
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction
                          variant="destructive"
                          onClick={() => void deleteEndpoint(endpoint.id)}
                        >
                          Delete webhook
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {creating && <CreateWebhook onClose={() => setCreating(false)} />}
      {secret && (
        <SigningSecret secret={secret} onClose={() => setSecret("")} />
      )}
      {history && (
        <DeliveryHistory
          endpoint={history}
          onClose={() => setHistory(undefined)}
        />
      )}
    </section>
  )
}

function CreateWebhook({ onClose }: { onClose: () => void }) {
  const create = useMutation(useNotificationsWebhooksCreate)
  const invalidate = useInvalidate()
  const [url, setUrl] = useState("")
  const [description, setDescription] = useState("")
  const [secret, setSecret] = useState("")
  const [error, setError] = useState("")

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setError("")
    try {
      const result = (await create.mutateAsync({
        body: { url: url.trim(), description: description.trim() },
      })) as WebhookEndpointSecret
      setSecret(result.secret)
      invalidate(useNotificationsWebhooksList)
    } catch (err) {
      setError(messageOf(err, "The webhook wasn't created."))
    } finally {
      create.reset()
    }
  }

  if (secret) return <SigningSecret secret={secret} onClose={onClose} />
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
          <DialogHeader>
            <DialogTitle>Add webhook</DialogTitle>
            <DialogDescription>
              Receive signed deployment results at a public HTTPS endpoint.
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="webhook-url">Endpoint URL</FieldLabel>
            <Input
              id="webhook-url"
              type="url"
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              placeholder="https://example.com/loomplane"
              required
              maxLength={2048}
              autoComplete="off"
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="webhook-description">Description</FieldLabel>
            <Input
              id="webhook-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              maxLength={200}
              placeholder="Release automation"
            />
          </Field>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending && <Spinner />}Create webhook
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SigningSecret({
  secret,
  onClose,
}: {
  secret: string
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState("")
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret)
      setCopied(true)
    } catch {
      setError("Copy failed. Select the secret and copy it manually.")
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Copy your signing secret</DialogTitle>
          <DialogDescription>
            You can see this secret only once. Save it in your receiver's secret
            store to verify Relay signatures.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 gap-2">
          <Input
            aria-label="Signing secret"
            className="min-w-0 font-mono text-xs"
            readOnly
            value={secret}
            autoComplete="off"
            onFocus={(event) => event.target.select()}
          />
          <Button
            variant="outline"
            size="icon"
            onClick={() => void copy()}
            aria-label="Copy signing secret"
          >
            {copied ? <CheckIcon /> : <CopyIcon />}
          </Button>
        </div>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DeliveryHistory({
  endpoint,
  onClose,
}: {
  endpoint: { id: string; url: string }
  onClose: () => void
}) {
  const deliveries = useQuery(useNotificationsWebhooksDeliveries, {
    path: { id: endpoint.id },
  })
  const denied = useAuthorizationFailure(deliveries)
  useNotificationRefresh(deliveries.refetch)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Delivery history</DialogTitle>
          <DialogDescription className="break-all">
            {denied ? "Recent webhook delivery outcomes." : endpoint.url}
          </DialogDescription>
        </DialogHeader>
        <p className="text-xs text-muted-foreground">
          Relay retries transient failures automatically. Recent outcomes
          refresh every 10 seconds.
        </p>
        {deliveries.status === "error" && (
          <ReadError
            error={deliveries.error}
            fallback="Delivery history couldn't be loaded."
            retry={() => void deliveries.refetch().catch(() => undefined)}
            label="Retry deliveries"
          />
        )}
        {!deliveries.data && deliveries.status !== "error" && (
          <Skeleton className="h-32 w-full" />
        )}
        {deliveries.data &&
          deliveries.data.items.length === 0 &&
          deliveries.status !== "error" && (
            <ZeroState>
              <ZeroStateHeader>
                <ZeroStateMedia variant="icon">
                  <WebhookIcon />
                </ZeroStateMedia>
                <ZeroStateTitle>No deliveries yet</ZeroStateTitle>
                <ZeroStateDescription>
                  Deployment events sent to this endpoint appear here with their
                  delivery outcome.
                </ZeroStateDescription>
              </ZeroStateHeader>
            </ZeroState>
          )}
        {deliveries.data && !denied && deliveries.data.items.length > 0 && (
          <ul className="divide-y rounded-lg border">
            {deliveries.data.items.map((item) => (
              <li key={item.id} className="grid gap-1 px-3 py-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-sm font-medium break-all">
                    {item.eventType}
                  </span>
                  <Badge variant="outline">
                    {deliveryStatus(item.status, item.attempts)}
                  </Badge>
                </div>
                <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span>
                    {item.attempts}{" "}
                    {item.attempts === 1 ? "attempt" : "attempts"}
                  </span>
                  {item.statusCode > 0 && <span>HTTP {item.statusCode}</span>}
                  <time dateTime={item.createdAt}>
                    {formatDate(item.createdAt)}
                  </time>
                  {item.deliveredAt && (
                    <span>Delivered {formatDate(item.deliveredAt)}</span>
                  )}
                  {item.nextRetryAt && (
                    <span>
                      {item.attempts > 0 ? "Next retry" : "Next attempt"}{" "}
                      {formatDate(item.nextRetryAt)}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function emailStatus(status: string) {
  const labels: Record<string, string> = {
    pending: "Email pending",
    sent: "Email sent",
    failed: "Email failed",
    skipped: "Email skipped",
  }
  return labels[status]
}

function deliveryStatus(status: string, attempts: number) {
  if (status === "pending" && attempts > 0) return "Retrying"
  const labels: Record<string, string> = {
    pending: "Pending",
    queued: "Queued",
    delivering: "Delivering",
    retrying: "Retrying",
    delivered: "Delivered",
    succeeded: "Delivered",
    failed: "Failed permanently",
    dead_letter: "Failed permanently",
    dead: "Failed permanently",
  }
  return labels[status] ?? status
}
