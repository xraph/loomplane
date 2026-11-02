import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import { messageOf } from "@/client/errors"
import { useMutation } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import type { InventoryResource } from "@/lib/provider-resources"

export function ProviderCredentials({
  resource,
  observed,
  native = false,
  onSaved,
}: {
  resource: InventoryResource
  observed: boolean
  native?: boolean
  onSaved: () => Promise<unknown>
}) {
  const connect = useMutation(api.useProviderResourcesConnect)
  const [projectId, setProjectId] = useState("")
  const [environment, setEnvironment] = useState("")
  const [error, setError] = useState("")
  const [saved, setSaved] = useState(false)
  const ready =
    observed &&
    (native
      ? !!resource.apiAccountId && !!resource.externalId
      : !!projectId.trim() && !!environment.trim())
  const save = async () => {
    if (!ready || connect.isPending) return
    setError("")
    setSaved(false)
    try {
      await connect.mutateAsync({
        path: { resourceId: resource.id },
        body: native
          ? {
              apiAccess: {
                provider: resource.provider,
                kind:
                  resource.kind === "object-storage"
                    ? "cloudflare-r2"
                    : resource.kind,
                accountId: resource.apiAccountId,
                resourceId: resource.externalId,
                native: true,
              },
            }
          : {
              providerProjectId: projectId.trim(),
              providerEnvironment: environment.trim(),
            },
      })
      setSaved(true)
      await onSaved().catch(() =>
        setError(
          "Credentials were saved, but resource details could not be refreshed. Reopen this resource to check its status."
        )
      )
    } catch (cause) {
      setError(
        messageOf(
          cause,
          "Access could not be saved. Check the resource and provider permissions, then retry."
        )
      )
    }
  }
  return (
    <form
      className="grid gap-2 border-t pt-3"
      onSubmit={(event) => {
        event.preventDefault()
        void save()
      }}
    >
      {native ? (
        <p className="text-xs text-muted-foreground">
          Save a native binding for a Cloudflare Worker in this account. Your
          account API token stays in Loomplane.
        </p>
      ) : (
        <>
          <div className="grid gap-2 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="resource-provider-project">
                Existing Vercel project ID
              </Label>
              <Input
                id="resource-provider-project"
                value={projectId}
                onChange={(event) => setProjectId(event.target.value)}
                disabled={connect.isPending}
                placeholder="prj_…"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="resource-provider-environment">
                Provider environment
              </Label>
              <Input
                id="resource-provider-environment"
                value={environment}
                onChange={(event) => setEnvironment(event.target.value)}
                disabled={connect.isPending}
                placeholder="production"
              />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            Use an existing resource connection in this provider project and
            environment. Only its mapped application credentials are saved in
            Vault. Review a deployment to verify the backing connection.
          </p>
        </>
      )}
      <Button
        type="submit"
        size="sm"
        variant="outline"
        disabled={!ready || connect.isPending}
      >
        {connect.isPending
          ? "Saving…"
          : native
            ? "Use native binding"
            : "Save provider credentials"}
      </Button>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {saved && (
        <p role="status" className="text-[13px]">
          {native ? "Native binding saved" : "Provider credentials saved"}.
          Review a deployment to check compatibility and access.
        </p>
      )}
    </form>
  )
}
