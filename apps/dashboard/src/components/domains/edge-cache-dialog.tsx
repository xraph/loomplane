import { useEffect, useState } from "react"

import { messageOf } from "@/client/errors"
import {
  useDomainEdgeGet,
  useDomainEdgeUpdate,
  useDomainEdgePurge,
  useDomainEdgeRetry,
  useDomainRoutesList,
} from "@/client/forge/loomplane/hooks"
import type {
  EdgePolicy,
  EdgeRule,
  EdgeState,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
} from "@/components/zero-state"

const emptyRule = (): EdgeRule => ({
  pathPrefix: "/assets/",
  mode: "origin",
  edgeTtl: 0,
  browserTtl: 0,
})

export function EdgeCacheButton({
  projectId,
  routeId,
  hostname,
  writable,
}: {
  projectId: string
  routeId: string
  hostname: string
  writable: boolean
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        type="button"
        size="xs"
        variant="ghost"
        onClick={() => setOpen(true)}
        aria-label={`CDN and cache for ${hostname}`}
      >
        CDN &amp; cache
      </Button>
      {open && (
        <EdgeCacheDialog
          projectId={projectId}
          routeId={routeId}
          hostname={hostname}
          writable={writable}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

export function EdgeCacheDialog({
  projectId,
  routeId,
  hostname,
  writable,
  onClose,
}: {
  projectId: string
  routeId: string
  hostname: string
  writable: boolean
  onClose(): void
}) {
  const edge = useQuery(useDomainEdgeGet, { path: { projectId, routeId } })
  const invalidate = useInvalidate()
  const { refetch } = edge
  // Edge operations run independently of the dashboard's event stream.
  useEffect(() => {
    const timer = window.setInterval(() => {
      void refetch().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [refetch])
  useEffect(() => {
    if (edge.data?.status === "ready" || edge.data?.status === "disabled")
      invalidate(useDomainRoutesList)
  }, [edge.data?.status, invalidate])
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-[760px]">
        <DialogHeader className="gap-1">
          <DialogTitle>CDN &amp; edge cache</DialogTitle>
          <DialogDescription className="break-all">
            {hostname} · Cloudflare
          </DialogDescription>
        </DialogHeader>
        {edge.error ? (
          <QueryError
            error={edge.error}
            fallback="Couldn't read CDN settings."
            retry={refetch}
          />
        ) : edge.data ? (
          <EdgeCacheForm
            key={`${edge.data.revision}:${edge.data.status}`}
            state={edge.data}
            projectId={projectId}
            routeId={routeId}
            hostname={hostname}
            writable={writable}
            refresh={refetch}
          />
        ) : (
          <p role="status" className="text-sm text-muted-foreground">
            Loading CDN settings…
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}

function EdgeCacheForm({
  state,
  projectId,
  routeId,
  hostname,
  writable,
  refresh,
}: {
  state: EdgeState
  projectId: string
  routeId: string
  hostname: string
  writable: boolean
  refresh(): Promise<unknown>
}) {
  const [policy, setPolicy] = useState<EdgePolicy>(state.policy)
  const [urls, setURLs] = useState("")
  const [error, setError] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const update = useMutation(useDomainEdgeUpdate)
  const purge = useMutation(useDomainEdgePurge)
  const retry = useMutation(useDomainEdgeRetry)
  const working =
    submitted ||
    update.isPending ||
    purge.isPending ||
    retry.isPending ||
    state.status === "pending"
  const locked = !writable || working || state.status === "failed"
  const run = async (action: () => Promise<unknown>) => {
    setError("")
    try {
      await action()
      setSubmitted(true)
      await refresh()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The CDN operation wasn't accepted. Refresh and try again."
        )
      )
      await refresh().catch(() => undefined)
    }
  }
  const patchRule = (index: number, patch: Partial<EdgeRule>) =>
    setPolicy({
      ...policy,
      rules: policy.rules.map((rule, i) =>
        i === index ? { ...rule, ...patch } : rule
      ),
    })
  const addRule = () =>
    setPolicy({ ...policy, rules: [...policy.rules, emptyRule()] })
  const path = { projectId, routeId }
  const hasPublic = policy.rules.some((rule) => rule.mode === "public")
  const lastPurge =
    state.lastPurgeAt && !state.lastPurgeAt.startsWith("0001")
      ? new Date(state.lastPurgeAt).toLocaleString()
      : "None yet"

  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-muted px-3 py-2 text-xs">
        <span role="status">
          {working
            ? "Applying…"
            : state.status === "failed"
              ? "Needs attention"
              : state.status === "ready"
                ? "CDN enabled"
                : "CDN disabled"}
        </span>
        <span className="text-muted-foreground">Last purge: {lastPurge}</span>
      </div>
      {state.message && (
        <p role="alert" className="text-sm text-destructive">
          {state.message}
        </p>
      )}
      {state.invalidationError && (
        <p role="alert" className="text-sm text-destructive">
          Deployment purge failed: {state.invalidationError}. Loomplane will
          retry.
        </p>
      )}
      {state.status === "failed" && writable && (
        <Button
          size="sm"
          variant="outline"
          disabled={working}
          onClick={() =>
            void run(() =>
              retry.mutateAsync({ path, body: { revision: state.revision } })
            )
          }
        >
          Retry operation
        </Button>
      )}
      {state.status === "failed" && writable && (
        <Button
          size="sm"
          variant="outline"
          disabled={working}
          onClick={() =>
            void run(() =>
              update.mutateAsync({
                path,
                body: {
                  revision: state.revision,
                  policy: {
                    enabled: false,
                    invalidateOnDeploy: false,
                    rules: [],
                  },
                },
              })
            )
          }
        >
          Disable CDN and recover
        </Button>
      )}
      <form
        className="grid gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          void run(() =>
            update.mutateAsync({
              path,
              body: { revision: state.revision, policy },
            })
          )
        }}
      >
        <label className="flex items-center justify-between gap-3 text-sm font-medium">
          Enable global CDN
          <Switch
            checked={policy.enabled}
            onCheckedChange={(enabled) => setPolicy({ ...policy, enabled })}
            disabled={locked}
          />
        </label>
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer">Cloudflare requirements</summary>
          <p className="pt-1">
            Use a ready hostname on a managed Cloudflare domain. Its connection
            needs DNS Edit, Cache Rules Edit, Cache Purge and Zone Settings
            Read. Set Cloudflare SSL/TLS to Full (strict).
          </p>
        </details>
        <label className="flex items-center justify-between gap-3 text-sm">
          Purge this hostname after successful deployments
          <Switch
            checked={policy.invalidateOnDeploy}
            onCheckedChange={(invalidateOnDeploy) =>
              setPolicy({ ...policy, invalidateOnDeploy })
            }
            disabled={locked || !policy.enabled}
          />
        </label>
        <div className="flex items-center justify-between gap-2 border-t pt-3">
          <h3 className="text-sm font-medium">Cache rules</h3>
          {policy.rules.length > 0 && (
            <Button
              type="button"
              size="xs"
              variant="outline"
              disabled={locked || policy.rules.length >= 6}
              onClick={addRule}
            >
              Add rule
            </Button>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          Rules run from top to bottom; the last match wins. Unmatched paths,
          cookies, authorization headers and non-GET/HEAD requests bypass cache.
        </p>
        {policy.rules.length === 0 ? (
          <ZeroState variant="compact" className="items-start text-left">
            <ZeroStateHeader>
              <ZeroStateTitle>No cache rules</ZeroStateTitle>
              <ZeroStateDescription>
                CDN traffic goes to your origin until you add a path to cache.
              </ZeroStateDescription>
            </ZeroStateHeader>
            {writable && (
              <ZeroStateContent>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={addRule}
                  disabled={locked}
                >
                  Add rule
                </Button>
              </ZeroStateContent>
            )}
          </ZeroState>
        ) : (
          <div className="grid gap-2">
            {policy.rules.map((rule, i) => (
              <div
                key={i}
                className="grid grid-cols-[minmax(0,1fr)_auto] gap-2 rounded-lg border p-2.5 sm:grid-cols-[minmax(0,1fr)_160px_auto]"
              >
                <label className="col-span-2 grid gap-1 text-xs text-muted-foreground sm:col-span-1">
                  Path prefix {i + 1}
                  <Input
                    aria-label={`Path prefix ${i + 1}`}
                    value={rule.pathPrefix}
                    disabled={locked}
                    onChange={(event) =>
                      patchRule(i, { pathPrefix: event.target.value })
                    }
                    required
                  />
                </label>
                <label className="grid gap-1 text-xs text-muted-foreground">
                  Behavior
                  <select
                    aria-label={`Behavior ${i + 1}`}
                    className="h-9 min-w-0 rounded-md border bg-background px-2 text-sm text-foreground"
                    value={rule.mode}
                    disabled={locked}
                    onChange={(event) =>
                      patchRule(i, {
                        mode: event.target.value,
                        edgeTtl: event.target.value === "public" ? 3600 : 0,
                        browserTtl: 0,
                      })
                    }
                  >
                    <option value="origin">Respect origin</option>
                    <option value="public">Public content</option>
                    <option value="bypass">Bypass cache</option>
                  </select>
                </label>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="self-end"
                  aria-label={`Remove rule ${i + 1}`}
                  disabled={locked}
                  onClick={() =>
                    setPolicy({
                      ...policy,
                      rules: policy.rules.filter((_, index) => index !== i),
                    })
                  }
                >
                  Remove
                </Button>
                {rule.mode === "public" && (
                  <div className="col-span-2 grid grid-cols-2 gap-2 sm:col-span-3">
                    <label className="grid gap-1 text-xs text-muted-foreground">
                      Edge TTL (seconds)
                      <Input
                        aria-label={`Edge TTL ${i + 1}`}
                        type="number"
                        min={60}
                        max={31536000}
                        required
                        value={rule.edgeTtl}
                        disabled={locked}
                        onChange={(event) =>
                          patchRule(i, { edgeTtl: Number(event.target.value) })
                        }
                      />
                    </label>
                    <label className="grid gap-1 text-xs text-muted-foreground">
                      Browser TTL (seconds)
                      <Input
                        aria-label={`Browser TTL ${i + 1}`}
                        type="number"
                        min={0}
                        max={rule.edgeTtl}
                        required
                        value={rule.browserTtl}
                        disabled={locked}
                        onChange={(event) =>
                          patchRule(i, {
                            browserTtl: Number(event.target.value),
                          })
                        }
                      />
                    </label>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          Respect origin requires explicit cache headers from your app. Keep
          authenticated responses private. Cloudflare plan limits apply.
        </p>
        {hasPublic && (
          <p className="rounded-md bg-muted px-3 py-2 text-xs">
            Public content overrides origin cache headers, including private and
            no-store. Use it only for paths that return the same public content
            to every visitor. Purges cannot clear browser caches.
          </p>
        )}
        {writable && (
          <Button
            className="justify-self-end"
            size="sm"
            type="submit"
            disabled={locked}
          >
            Save CDN settings
          </Button>
        )}
      </form>
      {state.policy.enabled && (
        <div className="grid gap-2 border-t pt-3">
          <h3 className="text-sm font-medium">Purge cache</h3>
          <label className="grid gap-1 text-xs text-muted-foreground">
            Exact URLs, one per line
            <textarea
              aria-label="URLs to purge"
              className="min-h-16 w-full rounded-md border bg-background px-3 py-2 text-sm text-foreground"
              value={urls}
              placeholder={`https://${hostname}/logo.svg`}
              disabled={locked}
              onChange={(event) => setURLs(event.target.value)}
            />
          </label>
          {writable && (
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={locked || !urls.trim()}
                onClick={() =>
                  void run(() =>
                    purge.mutateAsync({
                      path,
                      body: {
                        revision: state.revision,
                        urls: urls
                          .split("\n")
                          .map((url) => url.trim())
                          .filter(Boolean),
                      },
                    })
                  )
                }
              >
                Purge URLs
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={locked}
                onClick={() =>
                  void run(() =>
                    purge.mutateAsync({
                      path,
                      body: { revision: state.revision, urls: [] },
                    })
                  )
                }
              >
                Purge hostname
              </Button>
            </div>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
