import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Profile,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { ConnectDialog } from "@/components/cloud-connections"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useOrganization } from "@/lib/organization"
import { ChangeReview } from "./change-review"
import { selectClass } from "./workspace"

export function TargetEditor({
  projectId,
  profile,
  target,
  onClose,
  onSaved,
}: {
  projectId: string
  profile: Profile
  target?: Target
  onClose: () => void
  onSaved: () => void
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const providers = useQuery(api.useProvidersList)
  const connections = useQuery(api.useConnectionsList)
  const create = useMutation(api.useCloudTargetsCreate)
  const update = useMutation(api.useCloudTargetsUpdate)
  const remove = useMutation(api.useCloudTargetsDelete)
  const [name, setName] = useState(target?.name ?? "")
  const [label, setLabel] = useState(target?.displayName ?? "")
  const [provider, setProvider] = useState(target?.provider ?? "")
  const [connectionId, setConnection] = useState(target?.connectionId ?? "")
  const [region, setRegion] = useState(target?.region ?? "")
  const [error, setError] = useState("")
  const [connecting, setConnecting] = useState(false)
  const [review, setReview] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [added, setAdded] = useState<Connection | null>(null)
  const selected = providers.data?.providers.find((p) => p.name === provider)
  const accounts = [
    ...(connections.data?.connections ?? []),
    ...(added && !connections.data?.connections.some((c) => c.id === added.id)
      ? [added]
      : []),
  ].filter((c) => c.status === "ready" && c.cloud === selected?.cloud)
  const changed =
    !!target &&
    (provider !== target.provider ||
      connectionId !== target.connectionId ||
      region !== target.region)
  const busy = create.isPending || update.isPending || remove.isPending
  const save = async () => {
    setError("")
    try {
      if (target)
        await update.mutateAsync({
          path: { id: projectId, cloudId: profile.id, targetId: target.id },
          body: { displayName: label.trim(), revision: target.revision },
        })
      else
        await create.mutateAsync({
          path: { id: projectId, cloudId: profile.id },
          body: {
            name: name.trim(),
            displayName: label.trim() || name.trim(),
            provider,
            connectionId,
            region,
          },
        })
      onSaved()
    } catch (err) {
      setError(messageOf(err, "The target wasn't saved."))
    }
  }
  const destroy = async () => {
    setError("")
    try {
      await remove.mutateAsync({
        path: { id: projectId, cloudId: profile.id, targetId: target!.id },
      })
      onSaved()
    } catch (err) {
      setError(
        messageOf(
          err,
          "The target couldn't be deleted. Remove its references first."
        )
      )
    }
  }
  return (
    <>
      <Dialog
        open={!review && !connecting}
        onOpenChange={(open) => !open && !busy && onClose()}
      >
        <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {target ? `Edit ${target.name}` : "Add target"}
            </DialogTitle>
            <DialogDescription>
              Targets place services. Resources and gateways keep their own
              placement.
            </DialogDescription>
          </DialogHeader>
          {(providers.status === "error" || connections.status === "error") && (
            <QueryError
              error={providers.error ?? connections.error}
              fallback="Providers and accounts couldn't be loaded."
              retry={() =>
                Promise.allSettled([providers.refetch(), connections.refetch()])
              }
            />
          )}
          <div className="grid gap-3">
            <div className="grid gap-2 sm:grid-cols-2">
              <label className="grid gap-1 text-xs">
                Target slug
                <Input
                  aria-label="Target slug"
                  disabled={!!target || !manage}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="frontend"
                />
              </label>
              <label className="grid gap-1 text-xs">
                Display name
                <Input
                  aria-label="Target display name"
                  disabled={!manage || busy}
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              </label>
            </div>
            <label className="grid gap-1 text-xs">
              Provider
              <select
                className={selectClass}
                aria-label="Target provider"
                disabled={!manage || busy}
                value={provider}
                onChange={(e) => {
                  setProvider(e.target.value)
                  setConnection("")
                  setRegion("")
                }}
              >
                <option value="">Choose provider</option>
                {providers.data?.providers
                  .filter((p) => p.available && p.runs?.length)
                  .map((p) => (
                    <option key={p.name} value={p.name}>
                      {p.title}
                    </option>
                  ))}
              </select>
            </label>
            {target && (
              <p className="text-xs break-words text-muted-foreground">
                Current desired account:{" "}
                {connections.data?.connections.find(
                  (c) => c.id === target.connectionId
                )?.name ?? target.connectionId}
              </p>
            )}
            <label className="grid gap-1 text-xs">
              Desired account
              <select
                className={selectClass}
                aria-label="Desired account"
                disabled={!manage || busy || !selected}
                value={connectionId}
                onChange={(e) => setConnection(e.target.value)}
              >
                <option value="">Choose a ready account</option>
                {accounts.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} ({c.account})
                  </option>
                ))}
              </select>
            </label>
            {manage && (
              <Button
                size="sm"
                variant="outline"
                disabled={!selected || busy}
                onClick={() => setConnecting(true)}
              >
                Connect a new account
              </Button>
            )}
            <label className="grid gap-1 text-xs">
              Region
              <Input
                aria-label="Target region"
                disabled={!manage || busy}
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                placeholder="Provider default"
              />
            </label>
          </div>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter className="flex-wrap">
            {target && manage && (
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => (deleting ? void destroy() : setDeleting(true))}
              >
                {deleting ? "Confirm delete target" : "Delete target"}
              </Button>
            )}
            <Button variant="outline" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            {manage && (
              <Button
                disabled={
                  busy ||
                  !provider ||
                  !accounts.some((c) => c.id === connectionId) ||
                  (!target && !/^[a-z][a-z0-9]*(-[a-z0-9]+)*$/.test(name)) ||
                  (changed &&
                    (!can("deployment:approve") ||
                      label !== target?.displayName))
                }
                onClick={() => (changed ? setReview(true) : void save())}
              >
                {changed ? "Review switch" : "Save target"}
              </Button>
            )}
          </DialogFooter>
          {changed && label !== target?.displayName && (
            <p className="text-xs text-muted-foreground">
              Save the display name before changing placement.
            </p>
          )}
        </DialogContent>
      </Dialog>
      {connecting && (
        <ConnectDialog
          open
          onOpenChange={setConnecting}
          cloud={selected?.cloud}
          onConnected={(c) => {
            if (c.status === "ready" && c.cloud === selected?.cloud) {
              setAdded(c)
              setConnection(c.id)
              setError("")
            } else
              setError(
                "The new account isn't ready for this provider. Check its permissions in Integrations."
              )
          }}
        />
      )}
      {review && target && (
        <ChangeReview
          projectId={projectId}
          title={`Switch ${target.name}`}
          change={{
            cloudId: profile.id,
            targetId: target.id,
            revision: target.revision,
            provider,
            connectionId,
            region,
          }}
          onClose={() => setReview(false)}
          onApplied={onSaved}
        />
      )}
    </>
  )
}
