import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import { messageOf } from "@/client/errors"
import { useMutation } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import type { InventoryResource } from "@/lib/provider-resources"

export function BucketCredentials({
  resource,
  observed,
  onSaved,
}: {
  resource: InventoryResource
  observed: boolean
  onSaved: () => Promise<unknown>
}) {
  const connect = useMutation(api.useProviderResourcesConnect)
  const [accessKeyId, setAccessKeyId] = useState("")
  const [secretAccessKey, setSecretAccessKey] = useState("")
  const [sessionToken, setSessionToken] = useState("")
  const [serviceAccountJson, setServiceAccountJson] = useState("")
  const [error, setError] = useState("")
  const [saved, setSaved] = useState(false)
  const aws = resource.provider === "aws"
  const ready = aws
    ? !!accessKeyId.trim() && !!secretAccessKey
    : !!serviceAccountJson.trim()
  const save = async () => {
    if (!ready || !observed || connect.isPending) return
    setError("")
    setSaved(false)
    try {
      await connect.mutateAsync({
        path: { resourceId: resource.id },
        body: {
          bucketAccess: {
            provider: resource.provider,
            bucket: resource.name,
            region: resource.region,
            ...(aws
              ? {
                  expectedOwner: resource.bucketOwner ?? "",
                  accessKeyId: accessKeyId.trim(),
                  secretAccessKey,
                  sessionToken,
                }
              : { serviceAccountJson }),
          },
        },
      })
      setAccessKeyId("")
      setSecretAccessKey("")
      setSessionToken("")
      setServiceAccountJson("")
      setSaved(true)
      await onSaved().catch(() => {
        setError(
          "Bucket credentials were saved, but resource details could not be refreshed. Reopen the resource to check its status."
        )
      })
    } catch (cause) {
      setError(
        messageOf(
          cause,
          "Bucket credentials could not be saved. Check your access and retry."
        )
      )
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
      <h3 className="font-medium">Application bucket credentials</h3>
      {aws ? (
        <>
          <div className="grid gap-1.5">
            <Label htmlFor="bucket-access-key">Access key ID</Label>
            <Input
              id="bucket-access-key"
              autoComplete="off"
              spellCheck={false}
              value={accessKeyId}
              onChange={(event) => setAccessKeyId(event.target.value)}
              disabled={connect.isPending}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="bucket-secret-key">Secret access key</Label>
            <Input
              id="bucket-secret-key"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              value={secretAccessKey}
              onChange={(event) => setSecretAccessKey(event.target.value)}
              disabled={connect.isPending}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="bucket-session-token">
              Session token (optional)
            </Label>
            <Input
              id="bucket-session-token"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              value={sessionToken}
              onChange={(event) => setSessionToken(event.target.value)}
              disabled={connect.isPending}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Use application keys scoped to this bucket. Your workload receives
            the bucket settings and AWS SDK credential variables.
          </p>
        </>
      ) : (
        <>
          <Label htmlFor="bucket-service-account">Service account JSON</Label>
          <Textarea
            id="bucket-service-account"
            autoComplete="off"
            spellCheck={false}
            value={serviceAccountJson}
            onChange={(event) => setServiceAccountJson(event.target.value)}
            disabled={connect.isPending}
            className="max-h-40 font-mono"
          />
          <p className="text-xs text-muted-foreground">
            Use an application service account with access to this bucket.
            Initialize your storage SDK from GOOGLE_SERVICE_ACCOUNT_JSON, which
            contains the key JSON.
          </p>
        </>
      )}
      <p className="text-xs text-muted-foreground">
        Saved in Vault. Deployment checks verify bucket listing access without
        writing objects. Write permissions are not tested.
      </p>
      <Button
        type="submit"
        size="sm"
        variant="outline"
        disabled={!ready || !observed || connect.isPending}
      >
        {connect.isPending ? "Saving…" : "Save bucket credentials"}
      </Button>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {saved && (
        <p role="status" className="text-[13px]">
          Bucket credentials saved. Review a deployment to check access.
        </p>
      )}
    </form>
  )
}
