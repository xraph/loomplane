import { ResourceFields, useResourceFields } from "./capacity-fields"
import { BoxIcon } from "lucide-react"
import { useState, type FormEvent } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Connection, Provider } from "@/client/forge/loomplane/types"
import { messageOf, statusOf } from "@/client/errors"
import { useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { ManagedDatabaseSettings } from "@/components/managed-database-settings"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
} from "@/components/zero-state"
import { standaloneService } from "@/lib/catalog"
import { freeName, takenNames } from "@/lib/catalog-add"
import {
  resourceOfferings,
  reviewProviderResource,
  type InventoryResource,
  type ResourceReviewInput,
} from "@/lib/provider-resources"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

/** list joins names the way a sentence would: "a", "a and b", "a, b and c". */
function list(names: string[]) {
  return names.length < 2
    ? names.join("")
    : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}

/** The kind and provider a catalog card opens creation with. */
export type ResourceOffer = { kind: string; provider: string }

export function ResourceReviewDialog({
  open,
  onOpenChange,
  resource,
  offer,
  providers,
  connections,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  resource?: InventoryResource
  offer?: ResourceOffer
  providers: Provider[]
  connections: Connection[]
}) {
  const projects = useQuery(api.useProjectsList)
  const [projectId, setProjectId] = useState(resource ? "" : "workspace")
  const [submitting, setSubmitting] = useState(false)
  const standalone = !resource && projectId === "workspace"
  const project =
    projects.data?.projects.find((p) => p.id === projectId) ??
    projects.data?.projects[0]
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!submitting) onOpenChange(next)
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {resource
              ? `Use ${resource.name} in deployment`
              : "Create a provider-managed resource"}
          </DialogTitle>
          <DialogDescription>
            {resource
              ? "Add a reference to your manifest, choose its consumer, then review the deployment plan."
              : "Your provider creates and runs it in your account. Review the creation plan before any provider changes are made."}
          </DialogDescription>
        </DialogHeader>
        {!standalone && projects.status === "error" ? (
          <QueryError
            error={projects.error}
            fallback="Projects could not be loaded."
            retry={projects.refetch}
          />
        ) : !standalone && !projects.data ? (
          <Skeleton className="h-8" />
        ) : !project && !standalone ? (
          <ZeroState variant="compact" preview={false}>
            <ZeroStateHeader>
              <ZeroStateTitle>Create a project first</ZeroStateTitle>
              <ZeroStateDescription>
                You need a project and environment to review this resource.
              </ZeroStateDescription>
            </ZeroStateHeader>
          </ZeroState>
        ) : (
          <>
            <div className="grid gap-1.5">
              <Label>{resource ? "Attach to project" : "Owner"}</Label>
              <Select
                disabled={submitting}
                value={standalone ? "workspace" : project?.id}
                onValueChange={(value) => setProjectId(value ?? "")}
              >
                <SelectTrigger
                  className="w-full"
                  size="sm"
                  aria-label={resource ? "Project" : "Owner"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {!resource && (
                    <SelectItem value="workspace">Workspace service</SelectItem>
                  )}
                  {projects.data?.projects.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {standalone ? (
              <ResourceReviewForm
                key="workspace"
                onSubmitting={setSubmitting}
                projectId=""
                environment="production"
                offer={offer}
                providers={providers}
                connections={connections}
                services={[]}
                taken={new Set()}
              />
            ) : (
              project && (
                <ResourceDestination
                  key={`${project.id}:${resource?.id ?? "new"}`}
                  submitting={submitting}
                  onSubmitting={setSubmitting}
                  projectId={project.id}
                  resource={resource}
                  offer={offer}
                  providers={providers}
                  connections={connections}
                />
              )
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
function ResourceDestination({
  submitting,
  onSubmitting,
  projectId,
  resource,
  offer,
  providers,
  connections,
}: {
  submitting: boolean
  onSubmitting: (value: boolean) => void
  projectId: string
  resource?: InventoryResource
  offer?: ResourceOffer
  providers: Provider[]
  connections: Connection[]
}) {
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: projectId },
  })
  const manifest = useQuery(api.useManifestsCurrent, {
    path: { id: projectId },
  })
  const [environment, setEnvironment] = useState("")
  const selected =
    environments.data?.environments.find((e) => e.name === environment) ??
    environments.data?.environments[0]
  if (environments.status === "error")
    return (
      <QueryError
        error={environments.error}
        fallback="Environments could not be loaded."
        retry={environments.refetch}
      />
    )
  const firstManifest =
    manifest.status === "error" && statusOf(manifest.error) === 404
  if (manifest.status === "error" && !firstManifest)
    return (
      <QueryError
        error={manifest.error}
        fallback="The manifest could not be loaded."
        retry={manifest.refetch}
      />
    )
  if (!environments.data || (!manifest.data && !firstManifest))
    return <Skeleton className="h-20" />
  if (!selected)
    return (
      <Alert>
        <AlertDescription>
          Your project needs an environment before you can review a resource.
        </AlertDescription>
      </Alert>
    )
  return (
    <>
      <div className="grid gap-1.5">
        <Label>Environment</Label>
        <Select
          disabled={submitting}
          value={selected.name}
          onValueChange={(value) => setEnvironment(value ?? "")}
        >
          <SelectTrigger size="sm" aria-label="Environment" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {environments.data.environments.map((e) => (
              <SelectItem key={e.name} value={e.name}>
                {e.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <ResourceReviewForm
        key={selected.name}
        onSubmitting={onSubmitting}
        projectId={projectId}
        environment={selected.name}
        resource={resource}
        offer={offer}
        providers={providers}
        connections={connections}
        taken={takenNames(manifest.data?.manifest ?? { version: 1 })}
        services={Object.keys(manifest.data?.manifest?.services ?? {})}
      />
    </>
  )
}
function ResourceReviewForm({
  onSubmitting,
  projectId,
  environment,
  resource,
  offer,
  providers,
  connections,
  taken,
  services,
}: {
  onSubmitting: (value: boolean) => void
  projectId: string
  environment: string
  resource?: InventoryResource
  offer?: ResourceOffer
  providers: Provider[]
  connections: Connection[]
  taken: Set<string>
  services: string[]
}) {
  const navigate = useNavigate()
  const [ownerId, setOwnerId] = useState("")
  const [savedScope, setSavedScope] = useState<{
    id: string
    catalog: boolean
  } | null>(null)
  const offerings = resourceOfferings(providers, connections)
  const kinds = [...new Set(offerings.map((entry) => entry.offer.kind))]
  const [kindValue, setKind] = useState(offer?.kind ?? "")
  const kind =
    resource?.kind ?? (kinds.includes(kindValue) ? kindValue : kinds[0])
  const options = offerings.filter((entry) => entry.offer.kind === kind)
  const [providerValue, setProvider] = useState(offer?.provider ?? "")
  const provider =
    options.find((entry) => entry.provider.name === providerValue)?.provider ??
    options[0]?.provider
  const accounts = options.filter(
    (entry) => entry.provider.name === provider?.name
  )
  const [accountValue, setAccount] = useState("")
  const account =
    accounts.find((entry) => entry.connection.id === accountValue) ??
    accounts[0]
  const [regionValue, setRegion] = useState("")
  const regions = provider?.regions ?? []
  const pinnedRegion =
    typeof account?.connection.details?.region === "string"
      ? account.connection.details.region
      : ""
  const region =
    pinnedRegion ||
    (regions.includes(regionValue) ? regionValue : (regions[0] ?? ""))
  const [versionValue, setVersion] = useState("")
  const profiles = account?.offer.managedProfiles ?? []
  const versions = profiles.length
    ? [...new Set(profiles.map((profile) => profile.version))]
    : (account?.offer.versions ?? [])
  const version = versions.includes(versionValue)
    ? versionValue
    : (versions[0] ?? "")
  const profile = profiles.find(
    (candidate) =>
      candidate.version === version &&
      candidate.access !== "cloud-sql-psc-connector"
  )
  const settingFields = resource ? [] : (account?.offer.settings ?? [])
  const form = useResourceFields(
    provider?.name,
    kind,
    account?.connection.id,
    region,
    settingFields,
    {},
    version
  )
  const settings = form.values
  const settingsValid = form.valid
  const [name, setName] = useState(() =>
    freeName(
      resource?.name
        .toLowerCase()
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/^[^a-z]+/, "")
        .slice(0, 35) ||
        (!projectId && kind) ||
        "resource",
      taken
    )
  )
  // With one service there's nothing to choose, so it starts ticked.
  const [consumers, setConsumers] = useState<string[]>(
    services.length === 1 ? services : []
  )
  const [pending, setPending] = useState(false)
  const [error, setError] = useState("")
  const [progress, setProgress] = useState("")
  const validName =
    /^[a-z][a-z0-9-]{0,39}$/.test(name) &&
    !name.includes("--") &&
    !taken.has(name)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (
      pending ||
      !validName ||
      !settingsValid ||
      !kind ||
      (!resource && !account)
    )
      return
    setPending(true)
    onSubmitting(true)
    setError("")
    const input: ResourceReviewInput = {
      projectId,
      environment,
      name,
      kind,
      ...(resource
        ? { existingResource: resource.id }
        : {
            provider: provider?.name,
            target: account?.connection.id,
            region,
            version,
            ...(settingFields.length ? { settings } : {}),
          }),
      ...(consumers.length ? { services: consumers } : {}),
    }
    try {
      if (savedScope) {
        const plan = await api.usePlansCreate({
          path: { id: savedScope.id },
          body: { environment, ...(savedScope.catalog && { catalog: true }) },
        })
        void navigate({ to: "/deployments/$id", params: { id: plan.id } })
        return
      }
      if (!projectId) {
        setProgress("Preparing the workspace service…")
        input.projectId = await standaloneService(
          name,
          `Standalone ${kind} resource`,
          setOwnerId,
          ownerId
        )
      }
      const result = await reviewProviderResource(
        input,
        setProgress,
        (catalog) => setSavedScope({ id: input.projectId, catalog })
      )
      void navigate({ to: "/deployments/$id", params: { id: result.planId } })
    } catch (cause) {
      setError(messageOf(cause, "The review could not be started."))
      setProgress("")
    } finally {
      setPending(false)
      onSubmitting(false)
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-3">
      {!projectId && (
        <p className="text-xs text-muted-foreground">
          After provisioning, choose Add service in each project that needs it,
          then open Workspace services.
        </p>
      )}
      <fieldset
        disabled={pending || !!savedScope}
        className="grid min-w-0 gap-3"
      >
        {!resource &&
          (!account ? (
            <Alert>
              <AlertDescription>
                No checked account supports creation from the installed provider
                registry. Connect or check an account in Integrations.
              </AlertDescription>
            </Alert>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1.5">
                  <Label>Resource kind</Label>
                  <Select
                    value={kind}
                    onValueChange={(value) => setKind(value ?? "")}
                  >
                    <SelectTrigger
                      size="sm"
                      aria-label="Resource kind"
                      className="w-full"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {kinds.map((entry) => (
                        <SelectItem key={entry} value={entry}>
                          {entry}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label>Provider</Label>
                  <Select
                    value={provider?.name ?? ""}
                    onValueChange={(value) => setProvider(value ?? "")}
                  >
                    <SelectTrigger
                      size="sm"
                      aria-label="Provider"
                      className="w-full"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {[
                        ...new Map(
                          options.map((entry) => [
                            entry.provider.name,
                            entry.provider,
                          ])
                        ).values(),
                      ].map((entry) => (
                        <SelectItem key={entry.name} value={entry.name}>
                          {entry.title}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="grid gap-1.5">
                <Label>Account</Label>
                <Select
                  value={account.connection.id}
                  onValueChange={(value) => setAccount(value ?? "")}
                >
                  <SelectTrigger
                    size="sm"
                    aria-label="Account"
                    className="w-full"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {accounts.map((entry) => (
                      <SelectItem
                        key={entry.connection.id}
                        value={entry.connection.id}
                      >
                        {entry.connection.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid grid-cols-2 gap-2">
                {region && (
                  <div className="grid gap-1.5">
                    <Label>Region</Label>
                    <Select
                      value={region}
                      onValueChange={(value) => setRegion(value ?? "")}
                      disabled={!!pinnedRegion}
                    >
                      <SelectTrigger
                        size="sm"
                        aria-label="Region"
                        className="w-full"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {(pinnedRegion ? [pinnedRegion] : regions).map(
                          (entry) => (
                            <SelectItem key={entry} value={entry}>
                              {entry}
                            </SelectItem>
                          )
                        )}
                      </SelectContent>
                    </Select>
                  </div>
                )}
                {versions.length > 0 && (
                  <div className="grid gap-1.5">
                    <Label>Version</Label>
                    <Select
                      value={version}
                      onValueChange={(value) => setVersion(value ?? "")}
                    >
                      <SelectTrigger
                        size="sm"
                        aria-label="Version"
                        className="w-full"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {versions.map((entry) => (
                          <SelectItem key={entry} value={entry}>
                            {entry}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                )}
              </div>
              <p className="text-xs text-muted-foreground">
                {account.offer.creates}
              </p>
              <ResourceFields form={form} disabled={pending} />
              {profile ? (
                <ManagedDatabaseSettings
                  profile={{
                    ...profile,
                    computeClass:
                      settings.compute_class ?? profile.computeClass,
                    storageGb: Number(
                      settings.storage_gib ?? profile.storageGb
                    ),
                  }}
                />
              ) : (
                <p className="text-xs text-muted-foreground">
                  Review your requested settings and provider costs in the plan
                  before approving. The adapter uses provider defaults for
                  settings you have not selected.
                </p>
              )}
            </>
          ))}
        <div className="grid gap-1.5">
          <Label htmlFor="resource-local-name">Manifest name</Label>
          <Input
            id="resource-local-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={pending}
          />
          {name && !validName && (
            <p className="text-xs text-destructive">
              Use a unique name with lowercase letters, digits and single
              hyphens, starting with a letter, up to 40 characters.
            </p>
          )}
        </div>
        {services.length > 0 && (
          <fieldset className="grid gap-2" disabled={pending}>
            <legend className="mb-1 text-[13px] leading-none font-medium">
              Services that use it
            </legend>
            <p className="text-xs text-muted-foreground">
              {consumers.length === 0
                ? "Tick each service that needs it. Leave them all clear to add it without one."
                : `${list(consumers)} will need ${name || "it"} in your manifest.`}
            </p>
            <div className="flex flex-wrap gap-2">
              {services.map((entry) => {
                const on = consumers.includes(entry)
                return (
                  <label
                    key={entry}
                    className={cn(
                      "flex cursor-pointer items-center gap-2 rounded-lg border px-2.5 py-1.5 text-[13px]",
                      on ? "border-brand-line bg-brand-soft" : "bg-card"
                    )}
                  >
                    <Checkbox
                      checked={on}
                      onCheckedChange={(checked) =>
                        setConsumers(
                          checked
                            ? services.filter(
                                (s) => s === entry || consumers.includes(s)
                              )
                            : consumers.filter((s) => s !== entry)
                        )
                      }
                    />
                    <BoxIcon className="size-3.5 text-muted-foreground" />
                    {entry}
                  </label>
                )
              })}
            </div>
          </fieldset>
        )}
        {resource && (
          <div className="grid gap-2 text-xs text-muted-foreground">
            <p>
              This reference is enabled in {environment} only. The service need
              is saved in your manifest; other environments must opt in before
              using it.
            </p>
            <p>
              For DigitalOcean and Azure, credentials and connectivity are
              checked from Loomplane. Configure your application's outbound
              network and the resource's firewall or trusted sources separately.
            </p>
            {resource.kind === "object-storage" && (
              <p>
                Bucket checks verify listing access without writing objects.
                Multiple bucket attachments receive separate variable prefixes.
              </p>
            )}
          </div>
        )}
      </fieldset>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {progress && (
        <p role="status" className="text-xs text-muted-foreground">
          {progress}
        </p>
      )}
      <Button
        type="submit"
        size="sm"
        disabled={
          pending ||
          !validName ||
          !settingsValid ||
          !kind ||
          (!resource && !account)
        }
      >
        {savedScope ? "Retry deployment plan" : "Review deployment plan"}
      </Button>
    </form>
  )
}
