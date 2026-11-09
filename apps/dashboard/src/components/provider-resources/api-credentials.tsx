import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import { messageOf } from "@/client/errors"
import { useMutation } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import type { InventoryResource } from "@/lib/provider-resources"

export function APICredentials({
  resource,
  observed,
  onSaved,
}: {
  resource: InventoryResource
  observed: boolean
  onSaved: () => Promise<unknown>
}) {
  const connect = useMutation(api.useProviderResourcesConnect)
  const [token, setToken] = useState("")
  const [error, setError] = useState("")
  const [saved, setSaved] = useState(false)
  const ready =
    observed &&
    !!resource.apiAccountId &&
    !!resource.externalId &&
    !!token.trim()
  const save = async () => {
    if (!ready || connect.isPending) return
    setError("")
    setSaved(false)
    try {
      await connect.mutateAsync({
        path: { resourceId: resource.id },
        body: {
          apiAccess: {
            provider: resource.provider,
            kind: resource.kind,
            accountId: resource.apiAccountId,
            resourceId: resource.externalId,
            token,
          },
        },
      })
      setToken("")
      setSaved(true)
      await onSaved().catch(() => {
        setError(
          "API credentials were saved, but resource details could not be refreshed. Reopen the resource to check its status."
        )
      })
    } catch (cause) {
      setError(
        messageOf(
          cause,
          "API credentials could not be saved. Check your access and retry."
        )
      )
    } finally {
      setToken("")
    }
  }
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        void save()
      }}
      className="grid gap-2 border-t pt-3"
    >
      <Label htmlFor="resource-api-token">Application API token</Label>
      <Input
        id="resource-api-token"
        type="password"
        autoComplete="new-password"
        spellCheck={false}
        value={token}
        onChange={(event) => setToken(event.target.value)}
        disabled={connect.isPending}
      />
      <p className="text-xs text-muted-foreground">
        Use an application token scoped to this resource. Saved in Vault. Your
        account connection token is never copied into a workload.
      </p>
      {!resource.apiAccountId && (
        <p className="text-xs text-muted-foreground">
          Sync this resource to verify its API account before saving
          credentials.
        </p>
      )}
      <Button
        type="submit"
        size="sm"
        variant="outline"
        disabled={!ready || connect.isPending}
      >
        {connect.isPending ? "Saving…" : "Save API credentials"}
      </Button>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {saved && (
        <p role="status" className="text-[13px]">
          API credentials saved. Review a deployment to check access.
        </p>
      )}
    </form>
  )
}
