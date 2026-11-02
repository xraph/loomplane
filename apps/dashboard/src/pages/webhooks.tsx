// Workspace webhooks: endpoints Relay signs deployment events for, their
// one-time signing secrets and recent delivery outcomes. Administrators only.
import { CheckIcon, CopyIcon, PlusIcon } from "lucide-react"
import type { UseQueryResult } from "@forge-go/client-react"
import { type FormEvent, useState } from "react"

import { messageOf } from "@/client/errors"
import {
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
import { WebhooksIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { StatusPill } from "@/components/runs"
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
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { formatDate } from "@/lib/format"
import {
  ReadError,
  useAuthorizationFailure,
  useNotificationRefresh,
} from "@/lib/notifications"
import { useOrganization } from "@/lib/organization"

export function Webhooks() {
  const { session, can } = useOrganization()
  return (
    <>
      <SiteHeader crumbs={[{ label: "Webhooks" }]} />
      <Page className="max-w-[980px]">
        <PageHeading
          title="Webhooks"
          description="Send signed deployment results from this workspace to your own endpoints."
        />
        {can("member:manage") ? (
          <Endpoints key={session.orgId} />
        ) : (
          <ZeroState className="py-10">
            <ZeroStateHeader>
              <ZeroStateMedia variant="icon">
                <WebhooksIcon />
              </ZeroStateMedia>
              <ZeroStateTitle>Webhooks are for administrators</ZeroStateTitle>
              <ZeroStateDescription>
                Ask a workspace administrator to add or change an endpoint.
              </ZeroStateDescription>
            </ZeroStateHeader>
          </ZeroState>
        )}
      </Page>
    </>
  )
}

function Endpoints() {
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
    <section
      aria-label="Webhook endpoints"
      className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border"
    >
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <div className="grid gap-0.5">
          <h2 className="text-[14px] font-semibold">Endpoints</h2>
          <span className="text-[12.5px] text-muted-foreground">
            Relay signs deployment events and retries delivery failures.
          </span>
        </div>
        {!denied && (
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon />
            Add webhook
          </Button>
        )}
      </div>
      {error && (
        <Alert variant="destructive" className="m-4">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {endpoints.status === "error" && (
        <div className="p-4">
          <ReadError
            error={endpoints.error}
            fallback="Webhooks couldn't be loaded."
            retry={() => void endpoints.refetch().catch(() => undefined)}
            label="Retry webhooks"
          />
        </div>
      )}
      {!endpoints.data && endpoints.status !== "error" && (
        <div className="p-4">
          <Skeleton className="h-20 w-full" />
        </div>
      )}
      {endpoints.data &&
        endpoints.data.items.length === 0 &&
        endpoints.status !== "error" && (
          <>
            <ZeroState surface={false} className="py-8">
              <ZeroStateHeader>
                <ZeroStateMedia variant="icon">
                  <WebhooksIcon />
                </ZeroStateMedia>
                <ZeroStateTitle>No webhooks configured</ZeroStateTitle>
                <ZeroStateDescription>
                  Add a public HTTPS endpoint to receive deployment results for
                  this workspace.
                </ZeroStateDescription>
              </ZeroStateHeader>
            </ZeroState>
          </>
        )}
      {endpoints.data && !denied && endpoints.data.items.length > 0 && (
        <>
          <ul className="divide-y">
            {endpoints.data.items.map((endpoint) => (
              <li
                key={endpoint.id}
                className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
              >
                <div className="grid min-w-0 flex-1 basis-48 gap-0.5">
                  <span className="font-mono text-[12.5px] font-medium break-all">
                    {endpoint.url}
                  </span>
                  <span className="text-xs break-words text-muted-foreground">
                    {endpoint.description || "Deployment events"}
                  </span>
                </div>
                <StatusPill tone={endpoint.enabled ? "live" : "waiting"}>
                  {endpoint.enabled
                    ? "Delivering"
                    : "Disabled by delivery policy"}
                </StatusPill>
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
                          variant="outline"
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
                          variant="outline"
                          size="sm"
                          className="text-destructive hover:text-destructive"
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
        </>
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
      <DialogContent className="sm:max-w-[520px]">
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
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle>Copy your signing secret</DialogTitle>
          <DialogDescription>
            You can see this secret only once. Save it in your receiver's secret
            store to verify Relay signatures.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 gap-1.5">
          <Input
            aria-label="Signing secret"
            className="h-9 min-w-0 border-transparent bg-muted font-mono text-[12.5px]"
            readOnly
            value={secret}
            autoComplete="off"
            onFocus={(event) => event.target.select()}
          />
          <Button
            variant="outline"
            className="h-9"
            onClick={() => void copy()}
            aria-label="Copy signing secret"
          >
            {copied ? <CheckIcon /> : <CopyIcon />}
            {copied ? "Copied" : "Copy"}
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
                  <WebhooksIcon />
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
                  <span className="font-mono text-[12.5px] font-medium break-all">
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
