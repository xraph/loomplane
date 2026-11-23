import { type ReactNode, useState } from "react"
import { toast } from "sonner"
import { messageOf, statusOf } from "@/client/errors"
import {
  useBindingsList,
  useConnectionsList,
  useEnvironmentConnectionsGet,
  useEnvironmentsList,
  useManifestsCurrent,
  useManifestsSave,
  useProviderResourcesList,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import type {
  BindingList,
  ConnectionList,
  EnvironmentConnections,
  Manifest,
  ProviderList,
  ProviderResource,
  ProviderResourceList,
  Revision,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { RowMenu } from "@/components/cloud-targets/cloud-page"
import { PlacementPanel } from "@/components/deployment-targets"
import {
  ResourceFields,
  useResourceFields,
} from "@/components/provider-resources/capacity-fields"
import { QueryError } from "@/components/query-error"
import { Wiring } from "@/components/service-wiring"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { entries, envVarFor } from "@/lib/add-service"
import { resourceLabel } from "@/lib/architecture"
import { useOrganization } from "@/lib/organization"
import { placementSettings, type PlacementRow } from "@/lib/placement"
import { providerResourceReadiness } from "@/lib/provider-resources"
import {
  compatibleResourceKind,
  existingResourceId,
  replaceResource,
  resourceConsumers,
  resourceEnabledIn,
  updateResourceManifest,
} from "@/lib/replace-resource"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

export type ResourceEditMode = "placement" | "modify" | "replace" | "remove"

export function DeploymentResourceActions({
  projectId,
  environment,
  name,
  onSaved,
  replan,
}: {
  projectId: string
  environment: string
  name: string
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
}) {
  const [edit, setEdit] = useState<ResourceEditMode | null>(null)
  const close = () => setEdit(null)
  return (
    <>
      <RowMenu label={`Manage ${name}`}>
        <DropdownMenuItem onClick={() => setEdit("placement")}>
          Change placement
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => setEdit("modify")}>
          Modify settings
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => setEdit("replace")}>
          Use existing resource
        </DropdownMenuItem>
        <DropdownMenuItem
          variant="destructive"
          onClick={() => setEdit("remove")}
        >
          Remove from manifest
        </DropdownMenuItem>
      </RowMenu>
      {edit && (
        <ResourceEditDialog
          projectId={projectId}
          environment={environment}
          name={name}
          edit={edit}
          close={close}
          onSaved={onSaved}
          replan={replan}
        />
      )}
    </>
  )
}

type EditDialogProps = {
  projectId: string
  environment: string
  name: string
  edit: ResourceEditMode
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
}

export function ResourceEditDialog(props: EditDialogProps) {
  const { projectId, environment, name, edit, close, onSaved, replan } = props
  const current = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const envs = useQuery(useEnvironmentsList, { path: { id: projectId } })
  const [busy, setBusy] = useState(false)
  const revision = current.data
  const manifest = revision?.manifest
  const entry = manifest?.resources?.[name] as
    | Record<string, unknown>
    | undefined
  const isExisting = !!existingResourceId(entry)
  const envList = envs.data?.environments ?? []
  const envNames = envList.map((e) => e.name)
  const envsPending = envs.status === "pending"

  if (edit === "placement" && revision && manifest && entry && !isExisting) {
    return (
      <PlacementPanel
        projectId={projectId}
        environment={environment}
        environments={envList}
        manifest={manifest}
        revision={revision.revision}
        row={{
          name,
          section: "resources",
          kind: String(entry.kind ?? ""),
          entry,
        }}
        mode="save"
        saveLabel={replan ? "Save and plan again" : undefined}
        onClose={close}
        onSaved={async () => {
          onSaved?.()
          if (replan) await replan(resourceConsumers(manifest, name).services)
        }}
      />
    )
  }

  const title =
    edit === "remove"
      ? `Remove ${name} from the manifest?`
      : edit === "replace"
        ? `Replace ${name}`
        : edit === "modify"
          ? `Modify ${name}`
          : `Placement of ${name}`

  return (
    <Dialog open onOpenChange={(open) => !open && !busy && close()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {revision
              ? `Editing loomplane.yaml revision ${revision.revision}. Changes apply across environments when their plans are approved.`
              : "Loading the current manifest."}
          </DialogDescription>
        </DialogHeader>
        {current.status === "error" ? (
          <QueryError
            error={current.error}
            retry={current.refetch}
            fallback="Couldn't load the manifest."
          />
        ) : !revision || !manifest ? (
          <p role="status">Loading…</p>
        ) : !entry ? (
          <p>{name} is no longer in the current manifest.</p>
        ) : edit === "placement" ? (
          <ExistingResourcePlacement
            key={revision.revision}
            projectId={projectId}
            environment={environment}
            envNames={envNames}
            envsPending={envsPending}
            name={name}
            revision={revision}
            manifest={manifest}
            busy={busy}
            setBusy={setBusy}
            close={close}
            onSaved={onSaved}
            replan={replan}
            refetchCurrent={current.refetch}
          />
        ) : edit === "modify" ? (
          <ResourceModify
            key={revision.revision}
            projectId={projectId}
            environment={environment}
            name={name}
            revision={revision}
            manifest={manifest}
            isExisting={isExisting}
            busy={busy}
            setBusy={setBusy}
            close={close}
            onSaved={onSaved}
            replan={replan}
            refetchCurrent={current.refetch}
          />
        ) : (
          <ResourceChange
            key={revision.revision}
            projectId={projectId}
            environment={environment}
            envNames={envNames}
            envsPending={envsPending}
            name={name}
            edit={edit}
            revision={revision}
            manifest={manifest}
            busy={busy}
            setBusy={setBusy}
            close={close}
            onSaved={onSaved}
            replan={replan}
            refetchCurrent={current.refetch}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ExistingResourcePlacement({
  projectId,
  environment,
  envNames,
  envsPending,
  name,
  revision,
  manifest,
  busy,
  setBusy,
  close,
  onSaved,
  replan,
  refetchCurrent,
}: {
  projectId: string
  environment: string
  envNames: string[]
  envsPending: boolean
  name: string
  revision: Revision
  manifest: Manifest
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
}) {
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const entry = manifest.resources![name] as Record<string, unknown>
  const initialEnabled = resourceEnabledIn(entry, environment)
  const [enabled, setEnabled] = useState(initialEnabled)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")
  const dirty = enabled !== initialEnabled
  const consumers = resourceConsumers(manifest, name)

  const submit = async () => {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      if (!saved) {
        const next = updateResourceManifest(manifest, name, environment, {
          enabled,
          environments: envNames,
        })
        await save.mutateAsync({
          path: { id: projectId },
          body: { baseRevision: revision.revision, manifest: next },
        })
        setSaved(true)
        invalidate(useManifestsCurrent)
        onSaved?.()
        if (!replan) {
          toast.success(`Saved where ${name} runs in ${environment}`)
        }
      }
      if (replan) await replan(consumers.services)
      close()
    } catch (err) {
      setError(messageOf(err, "Couldn't save the placement change."))
      if (statusOf(err) === 409) void refetchCurrent()
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <label className="flex items-center justify-between gap-3 text-[13px] font-medium">
        Runs in {environment}
        <Switch
          size="sm"
          aria-label={`Enable ${name} in ${environment}`}
          checked={enabled}
          disabled={busy || saved}
          onCheckedChange={setEnabled}
        />
      </label>
      <p className="text-sm text-muted-foreground">
        The connected provider manages this resource&apos;s placement. Choose
        Use existing resource to switch it.
      </p>
      {error && <p role="alert">{error}</p>}
      <DialogFooter>
        <Button variant="outline" disabled={busy} onClick={close}>
          Cancel
        </Button>
        <Button
          disabled={busy || envsPending || (!saved && !dirty)}
          onClick={() => void submit()}
        >
          {replan ? "Save and plan again" : "Save manifest"}
        </Button>
      </DialogFooter>
    </>
  )
}

type QuerySlice<T> = {
  data?: T
  status: string
  error?: unknown
  refetch: () => Promise<unknown>
}

function ResourceModify({
  isExisting,
  ...rest
}: {
  projectId: string
  environment: string
  name: string
  revision: Revision
  manifest: Manifest
  isExisting: boolean
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
}) {
  if (isExisting) {
    return <ResourceModifyBody {...rest} isExisting />
  }
  return <ManagedResourceModify {...rest} />
}

function ManagedResourceModify(props: {
  projectId: string
  environment: string
  name: string
  revision: Revision
  manifest: Manifest
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
}) {
  const providers = useQuery(useProvidersList)
  const connections = useQuery(useConnectionsList)
  const bindings = useQuery(useBindingsList, {
    path: { id: props.projectId },
    query: { environment: props.environment },
  })
  const envConnections = useQuery(useEnvironmentConnectionsGet, {
    path: { id: props.projectId, environment: props.environment },
  })
  return (
    <ResourceModifyBody
      {...props}
      isExisting={false}
      providers={providers}
      connections={connections}
      bindings={bindings}
      envConnections={envConnections}
    />
  )
}

function ResourceModifyBody({
  projectId,
  environment,
  name,
  revision,
  manifest,
  isExisting,
  busy,
  setBusy,
  close,
  onSaved,
  replan,
  refetchCurrent,
  providers,
  connections,
  bindings,
  envConnections,
}: {
  projectId: string
  environment: string
  name: string
  revision: Revision
  manifest: Manifest
  isExisting: boolean
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
  providers?: QuerySlice<ProviderList>
  connections?: QuerySlice<ConnectionList>
  bindings?: QuerySlice<BindingList>
  envConnections?: QuerySlice<EnvironmentConnections>
}) {
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const entry = manifest.resources![name] as Record<string, unknown>
  const entryKind = String(entry.kind ?? "")
  const consumers = resourceConsumers(manifest, name)
  const row: PlacementRow = {
    section: "resources",
    name,
    kind: entryKind,
    entry,
  }
  const placement = placementSettings(manifest, row, environment)
  const providerObj = providers?.data?.providers?.find(
    (p) => p.name === placement.provider
  )
  const offer = providerObj?.provisions?.find((o) => o.kind === entryKind)
  const versions = offer?.versions ?? []
  const binding = bindings?.data?.bindings?.find(
    (b) =>
      b.name === name &&
      b.subject === "resource" &&
      b.provider === providerObj?.name
  )
  const readyAccounts = (connections?.data?.connections ?? []).filter(
    (c) => c.status === "ready" && c.cloud === providerObj?.cloud
  )
  const envAccountId = providerObj?.cloud
    ? String(envConnections?.data?.connections?.[providerObj.cloud] ?? "")
    : ""
  const accountId =
    binding?.connectionId ||
    placement.target ||
    envAccountId ||
    (readyAccounts.length === 1 ? readyAccounts[0].id : undefined)
  const account = readyAccounts.find((c) => c.id === accountId)
  const region =
    binding?.region ||
    placement.region ||
    account?.details?.region ||
    providerObj?.regions?.[0] ||
    ""
  const initialVersion =
    typeof entry.version === "string" ? entry.version : ""
  const envOverrides = (
    entry.environments as
      | Record<string, { settings?: Record<string, string> } | undefined>
      | undefined
  )?.[environment]
  const initialSettings = {
    ...((entry.settings as Record<string, string> | undefined) ?? {}),
    ...(envOverrides?.settings ?? {}),
  }

  const [version, setVersion] = useState(initialVersion)
  const [neededBy, setNeededBy] = useState<string[]>(consumers.services)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")

  const capacity = useResourceFields(
    providerObj?.name,
    entryKind,
    accountId,
    region,
    !isExisting ? (offer?.settings ?? []) : [],
    initialSettings,
    version || initialVersion,
    binding?.externalId?.split("/").at(-1)
  )

  const serviceEntries = entries(manifest).filter(
    (e) => e.section === "services"
  )
  const versionItems = [
    { value: "__default__", label: "Provider default" },
    ...(initialVersion && !versions.includes(initialVersion)
      ? [{ value: initialVersion, label: initialVersion }]
      : []),
    ...versions.map((v) => ({ value: v, label: v })),
  ]

  const versionChanged = !isExisting && version !== initialVersion
  const settingsChanged =
    !isExisting &&
    capacity.edited &&
    capacity.fields.some(
      (f) =>
        capacity.values[f.name] !== (initialSettings[f.name] ?? f.default ?? "")
    )
  const sorted = (xs: string[]) => [...xs].sort()
  const wiringChanged =
    JSON.stringify(sorted(neededBy)) !==
    JSON.stringify(sorted(consumers.services))
  const changed = versionChanged || settingsChanged || wiringChanged
  const settingsInvalid =
    !isExisting &&
    capacity.edited &&
    capacity.fields.length > 0 &&
    !capacity.valid
  const waitingForCatalog =
    !isExisting &&
    (providers?.status === "pending" || connections?.status === "pending")

  const submit = async () => {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      if (!saved) {
        const next = updateResourceManifest(manifest, name, environment, {
          ...(versionChanged ? { version } : {}),
          ...(settingsChanged ? { settings: capacity.values } : {}),
          ...(wiringChanged ? { neededBy } : {}),
        })
        await save.mutateAsync({
          path: { id: projectId },
          body: { baseRevision: revision.revision, manifest: next },
        })
        setSaved(true)
        invalidate(useManifestsCurrent)
        onSaved?.()
        if (!replan) {
          toast.success(`Saved settings for ${name}`)
        }
      }
      if (replan)
        await replan([...new Set([...consumers.services, ...neededBy])])
      close()
    } catch (err) {
      setError(messageOf(err, "Couldn't save the resource settings."))
      if (statusOf(err) === 409) void refetchCurrent()
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {providers?.status === "error" ? (
        <QueryError
          error={providers.error}
          retry={providers.refetch}
          fallback="Couldn't load provider settings."
        />
      ) : waitingForCatalog ? (
        <p role="status">Loading resource settings…</p>
      ) : (
        <div className="grid gap-4">
          {isExisting && (
            <p className="text-sm text-muted-foreground">
              Version and capacity are managed by the external provider.
            </p>
          )}
          {!isExisting && versions.length > 0 && (
            <div className="grid gap-1.5">
              <span className="text-[13px] leading-none font-medium">
                Version
              </span>
              <Select
                items={versionItems}
                value={version || "__default__"}
                disabled={busy || saved}
                onValueChange={(v) =>
                  setVersion(!v || v === "__default__" ? "" : v)
                }
              >
                <SelectTrigger className="w-full" aria-label="Version">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {versionItems.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          {!isExisting && capacity.fields.length > 0 && (
            <ResourceFields form={capacity} disabled={busy || saved} />
          )}
          <fieldset disabled={busy || saved} className="min-w-0">
            <Wiring
              label="Used by"
              help={`Each service picked gets ${name}'s address in ${envVarFor({ name, kind: entryKind }, "resources")}.`}
              options={serviceEntries}
              value={neededBy}
              onChange={setNeededBy}
              hint={() => envVarFor({ name, kind: entryKind }, "resources")}
            />
          </fieldset>
          {!isExisting &&
            versions.length === 0 &&
            capacity.fields.length === 0 &&
            serviceEntries.length === 0 && (
              <p className="text-sm text-muted-foreground">
                No additional settings or services are available to configure
                for {name}.
              </p>
            )}
        </div>
      )}
      {error && <p role="alert">{error}</p>}
      <DialogFooter>
        <Button variant="outline" disabled={busy} onClick={close}>
          Cancel
        </Button>
        <Button
          disabled={
            busy ||
            waitingForCatalog ||
            (!saved && (!changed || settingsInvalid))
          }
          onClick={() => void submit()}
        >
          {replan ? "Save and plan again" : "Save manifest"}
        </Button>
      </DialogFooter>
    </>
  )
}

function WorkspaceInventoryData({
  children,
}: {
  children: (inventory: QuerySlice<ProviderResourceList>) => ReactNode
}) {
  const inventory = useQuery(useProviderResourcesList)
  return <>{children(inventory)}</>
}

function ResourceChange(props: {
  projectId: string
  environment: string
  envNames: string[]
  envsPending: boolean
  name: string
  edit: "replace" | "remove"
  revision: Revision
  manifest: Manifest
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
}) {
  const canReadInventory = useOrganization().can("infrastructure:read")
  if (props.edit === "replace" && canReadInventory) {
    return (
      <WorkspaceInventoryData>
        {(inventory) => (
          <ResourceChangeBody
            {...props}
            canReadInventory
            inventory={inventory}
          />
        )}
      </WorkspaceInventoryData>
    )
  }
  return <ResourceChangeBody {...props} canReadInventory={canReadInventory} />
}

function ResourceChangeBody({
  projectId,
  environment,
  envNames,
  envsPending,
  name,
  edit,
  revision,
  manifest,
  busy,
  setBusy,
  close,
  onSaved,
  replan,
  refetchCurrent,
  canReadInventory,
  inventory,
}: {
  projectId: string
  environment: string
  envNames: string[]
  envsPending: boolean
  name: string
  edit: "replace" | "remove"
  revision: Revision
  manifest: Manifest
  busy: boolean
  setBusy: (v: boolean) => void
  close: () => void
  onSaved?: () => void
  replan?: (affectedServices: string[]) => Promise<void>
  refetchCurrent: () => Promise<unknown>
  canReadInventory: boolean
  inventory?: QuerySlice<ProviderResourceList>
}) {
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const [choice, setChoice] = useState("")
  const [ignoreKind, setIgnoreKind] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")
  const replacing = edit === "replace"
  const now = useNow(60000)

  const entriesMap = manifest.resources!
  const entry = entriesMap[name] as Record<string, unknown>
  const entryKind = String(entry.kind ?? "")
  const isExisting = !!existingResourceId(entry)
  const consumers = resourceConsumers(manifest, name)
  const projectOptions = Object.entries(entriesMap)
    .filter(
      ([key, value]) =>
        key !== name &&
        compatibleResourceKind(String(value?.kind ?? ""), entryKind) &&
        !consumers.resources.includes(key) &&
        resourceEnabledIn(value as Record<string, unknown>, environment)
    )
    .map(([key]) => ({ value: key, label: `${key} · this project` }))

  const allInventory = (inventory?.data?.resources ?? []).filter(
    (r): r is ProviderResource => !!r
  )
  const isUnattached = (r: ProviderResource) =>
    !r.consumers?.some((c) => c.projectId === projectId && c.name === name) &&
    !Object.values(entriesMap).some(
      (e) => existingResourceId(e as Record<string, unknown>) === r.id
    )
  const workspace = allInventory.filter(
    (r) =>
      compatibleResourceKind(r.kind, entryKind) &&
      providerResourceReadiness(r, now).discovery === "Observed" &&
      !!r.endpointId &&
      ![
        "native_binding_required",
        "identity_verification_needed",
      ].includes(r.connectionState) &&
      isUnattached(r)
  )
  const readyIds = new Set(workspace.map((r) => r.id))
  const unreadyCount = allInventory.filter(
    (r) =>
      r.status !== "missing" &&
      compatibleResourceKind(r.kind, entryKind) &&
      !readyIds.has(r.id) &&
      isUnattached(r)
  ).length
  const options = [
    ...projectOptions,
    ...workspace.map((r) => ({
      value: r.id,
      label: [r.name, r.provider, r.region].filter(Boolean).join(" · "),
    })),
  ]
  const usedBy = [...consumers.services, ...consumers.resources]
  const lastOfKind = !Object.entries(entriesMap).some(
    ([key, value]) =>
      key !== name &&
      compatibleResourceKind(String(value?.kind ?? ""), entryKind)
  )
  const alreadyIgnored = manifest.ignore?.resources?.includes(entryKind) ?? false

  const submit = async () => {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      if (!saved) {
        const replacement = replacing
          ? (workspace.find((r) => r.id === choice) ?? choice)
          : undefined
        const next = replaceResource(manifest, name, replacement, {
          environments: envNames,
          ignoreKind: !replacing && ignoreKind,
        })
        await save.mutateAsync({
          path: { id: projectId },
          body: { baseRevision: revision.revision, manifest: next },
        })
        setSaved(true)
        invalidate(useManifestsCurrent)
        onSaved?.()
        if (!replan) {
          toast.success(
            replacing
              ? `Replaced ${name} in loomplane.yaml`
              : `Removed ${name} from loomplane.yaml`
          )
        }
      }
      if (replan) await replan(consumers.services)
      close()
    } catch (err) {
      setError(messageOf(err, "Couldn't save the resource change."))
      if (statusOf(err) === 409) void refetchCurrent()
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {replacing && (
        <>
          <Select
            items={options}
            value={choice || null}
            onValueChange={(v) => setChoice(v ?? "")}
          >
            <SelectTrigger
              className="w-full"
              aria-label="Existing resource"
              disabled={busy || saved}
            >
              <SelectValue
                placeholder={`Choose an existing ${entryKind} resource`}
              />
            </SelectTrigger>
            <SelectContent>
              {options.map((o) => (
                <SelectItem value={o.value} key={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {canReadInventory &&
            (inventory?.status === "error" ? (
              <QueryError
                error={inventory.error}
                retry={inventory.refetch}
                fallback="Couldn't load workspace resources."
              />
            ) : !inventory?.data ? (
              <p role="status">Loading workspace resources…</p>
            ) : null)}
          {!options.length &&
            (!canReadInventory ||
              (inventory?.status !== "error" && !!inventory?.data)) && (
              <p className="text-sm text-muted-foreground">
                No compatible project resources or verified workspace resources
                are available.
              </p>
            )}
          {canReadInventory && unreadyCount > 0 && (
            <p className="text-xs text-muted-foreground">
              {unreadyCount === 1
                ? "1 compatible workspace resource needs sync or verified credentials in "
                : `${unreadyCount} compatible workspace resources need sync or verified credentials in `}
              <Link to="/integrations" className="underline underline-offset-4">
                Integrations
              </Link>{" "}
              before attaching.
            </p>
          )}
        </>
      )}
      {!!usedBy.length && (
        <p className="text-sm break-words">
          {replacing ? "Switch dependencies for" : "Remove the dependency from"}{" "}
          {usedBy.join(", ")}.
        </p>
      )}
      {!replacing && !!consumers.resources.length && (
        <p role="alert">
          Choose a replacement for {consumers.resources.join(", ")} first.
        </p>
      )}
      {!replacing && lastOfKind && !alreadyIgnored && (
        <label className="flex items-start gap-2 text-sm">
          <Checkbox
            checked={ignoreKind}
            disabled={busy || saved}
            onCheckedChange={(v) => setIgnoreKind(!!v)}
          />
          <span>
            Also ignore {resourceLabel(entryKind)} so future plans don&apos;t
            suggest adding it
          </span>
        </label>
      )}
      <p className="text-sm text-muted-foreground">
        {isExisting
          ? "Removing this attachment leaves the provider resource in place."
          : replacing
            ? `Replacing ${name} removes the manifest entry. If ${name} is already deployed, approving the plan can delete the replaced resource and its data.`
            : `If ${name} is already deployed, approving its deletion can delete the resource and its data.`}
      </p>
      {error && <p role="alert">{error}</p>}
      <DialogFooter>
        <Button variant="outline" disabled={busy} onClick={close}>
          Cancel
        </Button>
        <Button
          disabled={
            busy ||
            envsPending ||
            (!saved &&
              (replacing
                ? !options.some((o) => o.value === choice)
                : !!consumers.resources.length))
          }
          onClick={() => void submit()}
        >
          {replan ? "Save and plan again" : "Save manifest"}
        </Button>
      </DialogFooter>
    </>
  )
}
