import { useState, type FormEvent } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Connection, Provider } from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { ManagedDatabaseSettings } from "@/components/managed-database-settings"
import { QueryError } from "@/components/query-error"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
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
import { freeName, takenNames } from "@/lib/catalog-add"
import {
  resourceOfferings,
  reviewProviderResource,
  type InventoryResource,
  type ResourceReviewInput,
} from "@/lib/provider-resources"
import { useNavigate } from "@/lib/router"

export function ResourceReviewDialog({
  open,
  onOpenChange,
  resource,
  providers,
  connections,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  resource?: InventoryResource
  providers: Provider[]
  connections: Connection[]
}) {
  const projects = useQuery(api.useProjectsList)
  const [projectId, setProjectId] = useState("")
  const project =
    projects.data?.projects.find((p) => p.id === projectId) ??
    projects.data?.projects[0]
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {resource
              ? `Use ${resource.name} in deployment`
              : "Create a resource"}
          </DialogTitle>
          <DialogDescription>
            {resource
              ? "Add a reference to your manifest, choose its consumer, then review the deployment plan."
              : "Choose a checked account and review its creation plan before approving any provider changes."}
          </DialogDescription>
        </DialogHeader>
        {projects.status === "error" ? (
          <QueryError
            error={projects.error}
            fallback="Projects could not be loaded."
            retry={projects.refetch}
          />
        ) : !projects.data ? (
          <Skeleton className="h-8" />
        ) : !project ? (
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
              <Label>Project</Label>
              <Select
                value={project.id}
                onValueChange={(value) => setProjectId(value ?? "")}
              >
                <SelectTrigger
                  className="w-full"
                  size="sm"
                  aria-label="Project"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {projects.data.projects.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <ResourceDestination
              key={`${project.id}:${resource?.id ?? "new"}`}
              projectId={project.id}
              resource={resource}
              providers={providers}
              connections={connections}
            />
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
function ResourceDestination({
  projectId,
  resource,
  providers,
  connections,
}: {
  projectId: string
  resource?: InventoryResource
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
  if (manifest.status === "error")
    return (
      <QueryError
        error={manifest.error}
        fallback="The manifest could not be loaded."
        retry={manifest.refetch}
      />
    )
  if (!environments.data || !manifest.data) return <Skeleton className="h-20" />
  if (!selected || !manifest.data.manifest)
    return (
      <Alert>
        <AlertDescription>
          Your project needs a saved manifest and environment before you can
          review a resource.
        </AlertDescription>
      </Alert>
    )
  return (
    <>
      <div className="grid gap-1.5">
        <Label>Environment</Label>
        <Select
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
        projectId={projectId}
        environment={selected.name}
        resource={resource}
        providers={providers}
        connections={connections}
        taken={takenNames(manifest.data.manifest)}
        services={Object.keys(manifest.data.manifest.services ?? {})}
      />
    </>
  )
}
function ResourceReviewForm({
  projectId,
  environment,
  resource,
  providers,
  connections,
  taken,
  services,
}: {
  projectId: string
  environment: string
  resource?: InventoryResource
  providers: Provider[]
  connections: Connection[]
  taken: Set<string>
  services: string[]
}) {
  const navigate = useNavigate()
  const offerings = resourceOfferings(providers, connections)
  const kinds = [...new Set(offerings.map((entry) => entry.offer.kind))]
  const [kindValue, setKind] = useState("")
  const kind =
    resource?.kind ?? (kinds.includes(kindValue) ? kindValue : kinds[0])
  const options = offerings.filter((entry) => entry.offer.kind === kind)
  const [providerValue, setProvider] = useState("")
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
  const [name, setName] = useState(() =>
    freeName(
      resource?.name
        .toLowerCase()
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/^[^a-z]+/, "")
        .slice(0, 35) || "resource",
      taken
    )
  )
  const [service, setService] = useState(services[0] ?? "")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState("")
  const [progress, setProgress] = useState("")
  const validName =
    /^[a-z][a-z0-9-]{0,39}$/.test(name) &&
    !name.includes("--") &&
    !taken.has(name)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending || !validName || !kind || (!resource && !account)) return
    setPending(true)
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
          }),
      ...(service ? { service } : {}),
    }
    try {
      const result = await reviewProviderResource(input, setProgress)
      void navigate({ to: "/deployments/$id", params: { id: result.planId } })
    } catch (cause) {
      setError(messageOf(cause, "The review could not be started."))
      setProgress("")
    } finally {
      setPending(false)
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-3">
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
            {profile ? (
              <ManagedDatabaseSettings profile={profile} />
            ) : (
              <p className="text-xs text-muted-foreground">
                Uses the adapter's provider defaults for capacity, access and
                retention. Check the requested settings and costs in your plan
                before approving.
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
            Use a unique name with lowercase letters, digits and single hyphens,
            starting with a letter, up to 40 characters.
          </p>
        )}
      </div>
      {services.length > 0 && (
        <div className="grid gap-1.5">
          <Label>Service that needs this resource</Label>
          <Select
            value={service}
            onValueChange={(value) => setService(value ?? "")}
          >
            <SelectTrigger
              size="sm"
              aria-label="Consumer service"
              className="w-full"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Add without a consumer</SelectItem>
              {services.map((entry) => (
                <SelectItem key={entry} value={entry}>
                  {entry}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      {resource && (
        <div className="grid gap-2 text-xs text-muted-foreground">
          <p>
            This reference is enabled in {environment} only. The service need is
            saved in your manifest; other environments must opt in before using
            it.
          </p>
          <p>
            For DigitalOcean and Azure, credentials and connectivity are checked
            from Loomplane. Configure your application's outbound network and
            the resource's firewall or trusted sources separately.
          </p>
          {resource.kind === "object-storage" && (
            <p>
              Bucket checks verify listing access without writing objects.
              Multiple bucket attachments receive separate variable prefixes.
            </p>
          )}
        </div>
      )}
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
        disabled={pending || !validName || !kind || (!resource && !account)}
      >
        Review deployment plan
      </Button>
    </form>
  )
}
