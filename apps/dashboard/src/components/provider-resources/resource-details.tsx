import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import { messageOf } from "@/client/errors"
import { useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { ProviderCredentials } from "@/components/provider-resources/provider-credentials"
import { APICredentials } from "@/components/provider-resources/api-credentials"
import { BucketCredentials } from "@/components/provider-resources/bucket-credentials"
import { StatusPill } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { formatDate } from "@/lib/format"
import {
  providerResourceReadiness,
  providerResourceAttachmentNote,
  providerResourceOutputs,
  type InventoryResource,
} from "@/lib/provider-resources"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

type ResourceDetailsProps = {
  id: string
  open: boolean
  onOpenChange: (open: boolean) => void
  canConnect: boolean
  canReview: boolean
  onAttach: (resource: InventoryResource) => void
  onSaved: () => Promise<unknown>
}

export function ResourceDetails(props: ResourceDetailsProps) {
  return <ResourceDetailsContent key={`${props.id}:${props.open}`} {...props} />
}

function ResourceDetailsContent({
  id,
  open,
  onOpenChange,
  canConnect,
  canReview,
  onAttach,
  onSaved,
}: ResourceDetailsProps) {
  const query = useQuery(api.useProviderResourcesGet, {
    path: { resourceId: id },
  })
  const connect = useMutation(api.useProviderResourcesConnect)
  const now = useNow()
  const [dsn, setDSN] = useState("")
  const [error, setError] = useState("")
  const [saved, setSaved] = useState(false)
  const resource = query.data
  const consumers = resource?.consumers ?? []
  const outputs = resource ? providerResourceOutputs(resource) : []
  const attachmentNote = resource
    ? providerResourceAttachmentNote(resource)
    : ""
  const readiness = resource
    ? providerResourceReadiness(resource, now)
    : undefined
  const save = async () => {
    setError("")
    setSaved(false)
    try {
      await connect.mutateAsync({ path: { resourceId: id }, body: { dsn } })
      setDSN("")
      setSaved(true)
      await Promise.all([query.refetch(), onSaved()])
    } catch (cause) {
      setDSN("")
      setError(
        messageOf(
          cause,
          "Credentials could not be saved. Check the resource and account, then retry."
        )
      )
    }
  }
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
        <SheetHeader className="border-b pr-12">
          <SheetTitle>{resource?.name ?? "Resource details"}</SheetTitle>
          <SheetDescription>
            Provider facts, connection access and current deployment consumers.
          </SheetDescription>
        </SheetHeader>
        <div className="grid gap-4 overflow-y-auto p-4">
          {query.status === "error" ? (
            <QueryError
              error={query.error}
              fallback="Resource details could not be loaded."
              retry={query.refetch}
            />
          ) : !resource || !readiness ? (
            <Skeleton className="h-32" />
          ) : (
            <>
              <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-[13px]">
                {[
                  ["Engine", `${resource.kind} ${resource.version ?? ""}`],
                  ["Provider", resource.provider],
                  [
                    "Account",
                    resource.apiAccountId ||
                      resource.bucketOwner ||
                      resource.accountIdentity,
                  ],
                  ["Region", resource.region || "Unspecified"],
                  ["Provider health", resource.status],
                  [
                    "Ownership",
                    resource.ownership === "external"
                      ? "External"
                      : "Loomplane",
                  ],
                  ["Discovery", readiness.discovery],
                  ["Last observation", formatDate(resource.observedAt)],
                  ["Credentials", readiness.credentials],
                  ["Network", readiness.network],
                  [
                    "Endpoint",
                    resource.host
                      ? `${resource.host}${resource.port ? `:${resource.port}` : ""}`
                      : "Not observed",
                  ],
                  ["Scope", resource.scope || "Unspecified"],
                ]
                  .filter(
                    ([label]) =>
                      resource.kind !== "object-storage" ||
                      ![
                        "Engine",
                        "Provider health",
                        "Network",
                        "Endpoint",
                        "Scope",
                      ].includes(label)
                  )
                  .map(([label, value]) => (
                    <div key={label} className="contents">
                      <dt className="text-muted-foreground">{label}</dt>
                      <dd className="min-w-0 break-words">{value}</dd>
                    </div>
                  ))}
              </dl>
              {resource.ownershipState === "unverified" && (
                <Alert>
                  <AlertDescription>
                    Loomplane could not verify a matching creation receipt.
                    Attachment does not transfer lifecycle ownership.
                  </AlertDescription>
                </Alert>
              )}
              {resource.connectionState === "identity_verification_needed" && (
                <Alert>
                  <AlertDescription>
                    The provider has not supplied a verified resource
                    incarnation. Sync and verify its identity before using it.
                  </AlertDescription>
                </Alert>
              )}
              {resource.identityScope === "account-name" && (
                <Alert>
                  <AlertDescription>
                    This attachment follows the bucket name in the verified AWS
                    account. If the same account recreates this bucket, the
                    attachment follows that name.
                  </AlertDescription>
                </Alert>
              )}
              {outputs.length > 0 && (
                <p className="text-xs break-words text-muted-foreground">
                  Deployment variables: {outputs.join(", ")}. Multiple
                  attachments receive separate variable prefixes. The deployment
                  review verifies API access without writing application data.
                </p>
              )}
              {attachmentNote && (
                <Alert>
                  <AlertDescription>{attachmentNote}</AlertDescription>
                </Alert>
              )}
              {canConnect &&
                resource.credentialMethods?.includes("native_binding") && (
                  <ProviderCredentials
                    resource={resource}
                    observed={readiness.discovery === "Observed"}
                    native
                    onSaved={async () => {
                      await Promise.all([query.refetch(), onSaved()])
                    }}
                  />
                )}
              {canConnect &&
                resource.credentialMethods?.includes(
                  "provider_credentials"
                ) && (
                  <ProviderCredentials
                    resource={resource}
                    observed={readiness.discovery === "Observed"}
                    onSaved={async () => {
                      await Promise.all([query.refetch(), onSaved()])
                    }}
                  />
                )}
              {canConnect &&
                resource.credentialMethods?.includes("api_access") && (
                  <APICredentials
                    resource={resource}
                    observed={
                      readiness.discovery === "Observed" &&
                      resource.connectionState !== "native_binding_required"
                    }
                    onSaved={async () => {
                      await Promise.all([query.refetch(), onSaved()])
                    }}
                  />
                )}
              {canConnect &&
                resource.credentialMethods?.includes("bucket_access") && (
                  <BucketCredentials
                    resource={resource}
                    observed={readiness.discovery === "Observed"}
                    onSaved={async () => {
                      await Promise.all([query.refetch(), onSaved()])
                    }}
                  />
                )}
              {canConnect && resource.credentialMethods?.includes("dsn") && (
                <form
                  onSubmit={(event) => {
                    event.preventDefault()
                    void save()
                  }}
                  className="grid gap-2 border-t pt-3"
                >
                  <Label htmlFor="provider-resource-dsn">
                    Application connection string
                  </Label>
                  <Input
                    id="provider-resource-dsn"
                    type="password"
                    autoComplete="new-password"
                    spellCheck={false}
                    value={dsn}
                    onChange={(event) => setDSN(event.target.value)}
                    placeholder="Enter a connection string"
                    disabled={connect.isPending}
                  />
                  <p className="text-xs text-muted-foreground">
                    Saved through Vault. Saving does not change the provider
                    password or verify a deployment network.
                  </p>
                  <Button
                    size="sm"
                    variant="outline"
                    type="submit"
                    disabled={
                      !dsn.trim() ||
                      connect.isPending ||
                      readiness.discovery !== "Observed"
                    }
                  >
                    {connect.isPending ? "Saving…" : "Save credentials"}
                  </Button>
                </form>
              )}
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              {saved && (
                <p role="status" className="text-[13px]">
                  Credentials saved. Review a deployment to verify network
                  access.
                </p>
              )}
              <section className="grid gap-2 border-t pt-3">
                <h3 className="font-medium">Deployment usage</h3>
                {consumers.length === 0 ? (
                  <p className="text-muted-foreground">
                    No recorded deployment bindings use this resource.
                  </p>
                ) : (
                  consumers.map((binding) => (
                    <div
                      key={binding.id}
                      className="flex items-center justify-between gap-2"
                    >
                      <Link
                        to="/projects/$id"
                        params={{ id: binding.projectId }}
                        className="min-w-0 truncate underline underline-offset-4"
                      >
                        {binding.name}
                      </Link>
                      <StatusPill tone="quiet">Bound</StatusPill>
                    </div>
                  ))
                )}
              </section>
              <Button
                size="sm"
                disabled={
                  !canReview ||
                  resource.connectionState === "native_binding_required" ||
                  !resource.endpointId ||
                  readiness.discovery !== "Observed" ||
                  resource.connectionState === "identity_verification_needed"
                }
                onClick={() => onAttach(resource)}
              >
                Use in deployment
              </Button>
              {!resource.endpointId && !attachmentNote && (
                <p className="text-xs text-muted-foreground">
                  Save supported application credentials before reviewing an
                  attachment. The deployment review shows where connection
                  checks run and which network access you need to configure.
                </p>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
