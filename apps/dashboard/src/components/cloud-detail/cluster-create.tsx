import { useEffect, useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Profile,
  ProjectGrant,
  ClusterObservation,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import {
  CloudEmpty,
  CloudError,
  Field,
  SetupReads,
} from "@/components/cloud-targets/infrastructure-ui"
import { CloudSelect } from "@/components/cloud-targets/cloud-select"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"

export function ClusterChoice({
  label,
  value,
  onChange,
  items,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  items: { value: string; label: string }[]
}) {
  return (
    <Field label={label}>
      <CloudSelect value={value} onValueChange={(v) => onChange(v ?? "")}>
        <SelectTrigger className="w-full">
          <SelectValue placeholder="Choose an option" />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </CloudSelect>
    </Field>
  )
}
const choices = (values: string[]) =>
  values.map((value) => ({ value, label: value }))
export function ClusterCreate({
  cloud,
  onClose,
  onSaved,
}: {
  cloud: Profile
  onClose: () => void
  onSaved: (id: string) => void
}) {
  const connections = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const [connection, setConnection] = useState("")
  const accounts = (connections.data?.connections ?? []).filter(
    (c) =>
      c.status === "ready" &&
      (providers.data?.providers ?? []).some(
        (p) =>
          p.cloud === c.cloud &&
          p.available &&
          p.infrastructure?.includes("cluster:discover")
      )
  )
  const selected = accounts.find((c) => c.id === connection)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Set up Kubernetes cluster</DialogTitle>
          <DialogDescription>
            Review account, fixed node capacity and runtime access before
            applying.
          </DialogDescription>
        </DialogHeader>
        <SetupReads
          reads={[
            { label: "Accounts", query: connections },
            { label: "Installed providers", query: providers },
          ]}
        >
          {!accounts.length ? (
            <CloudEmpty
              kind="compact"
              title="Connect a cluster-capable account"
            >
              A ready account with installed cluster discovery support is
              required. Provider support does not grant purchase authority.
            </CloudEmpty>
          ) : (
            <>
              <ClusterChoice
                label="Provider account"
                value={connection}
                onChange={setConnection}
                items={accounts.map((c) => ({ value: c.id, label: c.name }))}
              />
              {selected && (
                <ClusterSetup
                  key={selected.id}
                  cloud={cloud}
                  connection={selected}
                  onSaved={onSaved}
                />
              )}
            </>
          )}
        </SetupReads>
      </DialogContent>
    </Dialog>
  )
}
function ClusterSetup({
  cloud,
  connection,
  onSaved,
}: {
  cloud: Profile
  connection: Connection
  onSaved: (id: string) => void
}) {
  const [stage, setStage] = useState(0)
  const stageHeading = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    stageHeading.current?.focus()
  }, [stage])
  const [kind, setKind] = useState("create")
  const [region, setRegion] = useState("")
  const [name, setName] = useState("")
  const [version, setVersion] = useState("")
  const [network, setNetwork] = useState("")
  const [size, setSize] = useState("")
  const [count, setCount] = useState(1)
  const [project, setProject] = useState("")
  const [namespace, setNamespace] = useState("")
  const [image, setImage] = useState("")
  const [selected, setSelected] = useState<ClusterObservation | null>(null)
  const [managedID, setManagedID] = useState("")
  const [ack, setAck] = useState(false)
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const locked = useRef(false)
  const options = useQuery(api.useCloudClustersOptions, {
    path: { cloudId: cloud.id, connectionId: connection.id },
    query: { region },
  })
  const discovery = useQuery(api.useConnectionClustersList, {
    path: { connectionId: connection.id },
  })
  const resources = useQuery(api.useCloudClustersList, {
    path: { cloudId: cloud.id },
  })
  const grants = useQuery(api.useOrganizationCloudsListGrants, {
    path: { cloudId: cloud.id },
  })
  const projects = useQuery(api.useProjectsList)
  const policy = useQuery(api.useCloudCapacityGetPolicy, {
    path: { cloudId: cloud.id },
  })
  const preview = useMutation(api.useCloudClustersPreview)
  const allowed = (grants.data?.grants ?? []).filter(
    (g): g is ProjectGrant => !!g && !g.revoked && !g.targetIds.length
  )
  const grant = allowed.find((g) => g.projectId === project)
  const accountID = options.data?.accountId ?? ""
  const node = options.data?.nodeSizeFacts.find((n) => n.id === size)
  const create = kind === "create"
  const valid =
    stage === 0
      ? !!(accountID && (create ? region : selected))
      : stage === 1
        ? create
          ? !!(name && version && network && size && count > 0)
          : kind !== "adopt-managed" || !!managedID
        : stage === 2
          ? !!(
              grant &&
              namespace &&
              (!create || /^.+@sha256:[a-f0-9]{64}$/.test(image))
            )
          : ack
  const submit = async () => {
    if (locked.current || !grant || !options.data) return
    locked.current = true
    setBusy(true)
    setError("")
    try {
      const resource = resources.data?.items.find((r) => r.id === managedID)
      const ref = selected?.ref ?? {
        id: "",
        accountId: "",
        incarnation: "",
        region: "",
      }
      const result = await preview.mutateAsync({
        path: { cloudId: cloud.id },
        body: {
          kind,
          provider: connection.cloud,
          connectionId: connection.id,
          accountId: accountID,
          connectionGeneration: options.data.connectionGeneration,
          cloudRevision: cloud.revision,
          resourceId: resource?.id ?? "",
          resourceRevision: resource?.revision ?? 0,
          policyRevision: policy.data?.policy?.revision ?? 0,
          managementReachable: ack,
          acknowledgeUnknownCosts: ack,
          acknowledgeNodeCosts: ack,
          workerImage: image,
          optionsDigest: options.data.digest,
          optionsCheckedAt: options.data.checkedAt,
          ref: create
            ? { id: "", accountId: "", region: "", incarnation: "" }
            : ref,
          spec: {
            operationId: "",
            name: create ? name : "",
            region: create ? region : "",
            version: create ? version : "",
            networkId: create ? network : "",
            ha: false,
            nodePools: create
              ? [
                  {
                    name: "workers",
                    size,
                    count,
                    min: 0,
                    max: 0,
                    autoscale: false,
                  },
                ]
              : [],
            maintenance: { day: "", startTime: "" },
          },
          attachment: {
            targetSlug: namespace,
            cloudRevision: cloud.revision,
            resourceRevision: resource?.revision ?? 0,
            grantRevision: grant.revision,
            trustFingerprint: selected?.caHash ?? "",
            endpointFingerprint: "",
            projectId: project,
            namespace,
            namespaceUid: "",
            serviceAccountName: "loomplane-runtime",
            adoptNamespace: false,
            prerequisites: {
              requirePersistentStorage: false,
              requireBuilds: false,
              requireIngress: false,
              requireTls: false,
            },
          },
        },
      })
      onSaved(result.id)
    } catch (err) {
      setError(messageOf(err, "Cluster review failed."))
      setStale(statusOf(err) === 409)
    } finally {
      locked.current = false
      setBusy(false)
    }
  }
  return (
    <div className="grid gap-3">
      <h3
        ref={stageHeading}
        tabIndex={-1}
        className="text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {
          [
            "Account and location",
            "Cluster and nodes",
            "Runtime prerequisites",
            "Review setup",
          ][stage]
        }
      </h3>
      <ol
        aria-label="Setup stages"
        className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
      >
        {[
          "Account and location",
          "Cluster and nodes",
          "Runtime prerequisites",
          "Review",
        ].map((title, index) => (
          <li
            key={title}
            aria-current={stage === index ? "step" : undefined}
            className={stage === index ? "font-semibold text-foreground" : ""}
          >
            {index + 1}. {title}
          </li>
        ))}
      </ol>
      <CloudError message={error} />
      {stale && (
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void options.refetch()
            void policy.refetch()
            setStage(0)
            setStale(false)
            setError("")
            setAck(false)
          }}
        >
          Refresh options and review
        </Button>
      )}
      <SetupReads
        reads={[
          { label: "Cluster options", query: options },
          { label: "Project grants", query: grants },
          { label: "Projects", query: projects },
          { label: "Capacity policy", query: policy },
        ]}
      >
        {stage === 0 && (
          <div className="grid gap-3 sm:grid-cols-2">
            <ClusterChoice
              label="Setup mode"
              value={kind}
              onChange={setKind}
              items={[
                { value: "create", label: "Create managed cluster" },
                { value: "adopt-external", label: "Adopt external cluster" },
                {
                  value: "adopt-managed",
                  label: "Adopt existing managed cluster",
                },
              ]}
            />
            {create ? (
              <ClusterChoice
                label="Region"
                value={region}
                onChange={setRegion}
                items={choices(options.data?.regions ?? [])}
              />
            ) : (
              <SetupReads
                reads={[{ label: "Provider discovery", query: discovery }]}
              >
                <ClusterChoice
                  label="Existing cluster"
                  value={selected?.ref.id ?? ""}
                  onChange={(id) =>
                    setSelected(
                      discovery.data?.items.find((c) => c.ref.id === id) ?? null
                    )
                  }
                  items={(discovery.data?.items ?? []).map((c) => ({
                    value: c.ref.id,
                    label: `${c.name || c.ref.id} (${c.ref.region})`,
                  }))}
                />
                {discovery.data?.nextCursor && (
                  <p className="text-xs">
                    More provider clusters are available through CLI discovery
                    pagination.
                  </p>
                )}
              </SetupReads>
            )}
            <p className="text-xs text-muted-foreground sm:col-span-2">
              Installed provider support and account authority are checked
              separately.
              {accountID
                ? " Account identity is verified."
                : " Account identity is unknown."}{" "}
              Creating clusters is restricted to configured qualification
              operators.
            </p>
          </div>
        )}
        {stage === 1 &&
          (create ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Cluster name">
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </Field>
              <ClusterChoice
                label="Kubernetes version"
                value={version}
                onChange={setVersion}
                items={choices(options.data?.versions ?? [])}
              />
              <ClusterChoice
                label="Network"
                value={network}
                onChange={setNetwork}
                items={choices(options.data?.networks ?? [])}
              />
              <ClusterChoice
                label="Node size"
                value={size}
                onChange={setSize}
                items={choices(options.data?.nodeSizes ?? [])}
              />
              <Field label="Fixed worker count">
                <Input
                  type="number"
                  min={1}
                  value={count}
                  onChange={(e) => setCount(Number(e.target.value))}
                />
              </Field>
              <p className="text-xs text-muted-foreground">
                {node
                  ? `${node.cpus} CPUs, ${node.memoryMib} MiB RAM, ${node.diskGb} GB disk per node.`
                  : "Choose a size for live capacity facts."}{" "}
                Native autoscaling is unavailable in this release.
              </p>
            </div>
          ) : (
            <div className="grid gap-3">
              <p className="text-sm">
                {kind === "adopt-external"
                  ? "The provider account retains lifecycle ownership. Loomplane manages only the reviewed runtime attachment."
                  : "Managed adoption requires the saved creation receipt and exact existing resource revision."}
              </p>
              {kind === "adopt-managed" && (
                <SetupReads
                  reads={[{ label: "Managed resources", query: resources }]}
                >
                  <ClusterChoice
                    label="Managed resource receipt"
                    value={managedID}
                    onChange={setManagedID}
                    items={(resources.data?.items ?? [])
                      .filter(
                        (r) =>
                          r.ownership === "managed" &&
                          r.externalId === selected?.ref.id
                      )
                      .map((r) => ({
                        value: r.id,
                        label: r.observation?.name || r.id,
                      }))}
                  />
                </SetupReads>
              )}
            </div>
          ))}
        {stage === 2 && (
          <div className="grid gap-3 sm:grid-cols-2">
            <ClusterChoice
              label="Project runtime grant"
              value={project}
              onChange={setProject}
              items={allowed.map((g) => ({
                value: g.projectId,
                label:
                  projects.data?.projects.find((p) => p.id === g.projectId)
                    ?.name ?? g.projectId,
              }))}
            />
            <Field label="New namespace and target slug">
              <Input
                value={namespace}
                onChange={(e) => setNamespace(e.target.value)}
              />
            </Field>
            {create && (
              <Field label="Digest-pinned worker image">
                <Input
                  value={image}
                  placeholder="registry/image@sha256:..."
                  onChange={(e) => setImage(e.target.value)}
                />
              </Field>
            )}
            <p className="text-xs text-muted-foreground sm:col-span-2">
              A new project-owned namespace receives renewable runtime access.
              No addons are installed. This setup selects private workers; HTTP
              ingress, TLS, persistent storage and builds are not enabled.
              Adoption of existing ingress and TLS prerequisites is available
              through the typed CLI preview.
            </p>
            {!allowed.length && (
              <CloudEmpty kind="compact" title="A project grant is required">
                Grant this project access to the whole Cloud before creating its
                runtime target.
              </CloudEmpty>
            )}
          </div>
        )}
        {stage === 3 && (
          <div className="grid gap-2 text-sm">
            <p>
              {create ? name : selected?.name} ·{" "}
              {create ? region : selected?.ref.region} ·{" "}
              {create ? `${count} fixed ${size} nodes` : kind}
            </p>
            <p>
              Account {connection.name}. Kubernetes{" "}
              {create ? version : selected?.version}. Network{" "}
              {create ? network : "Existing provider network"}.
            </p>
            {create && (
              <p className="text-xs break-all">Worker image: {image}</p>
            )}
            <p>
              Target {namespace}; service account loomplane-runtime. Private
              worker permissions only.
            </p>
            <p>
              Project{" "}
              {projects.data?.projects.find((p) => p.id === project)?.name ??
                project}
              , namespace {namespace}.
            </p>
            <p>
              Nodes:{" "}
              {create && node?.hourlyUsd != null
                ? `$${(node.hourlyUsd * count).toFixed(4)} / hour`
                : "Price unknown"}
              . Additional provider charges are not estimated. Quota is checked
              during apply; unknown quota is not zero.
            </p>
            <p className="text-xs text-muted-foreground">
              Cloud revision {cloud.revision}; grant revision {grant?.revision};
              capacity policy revision{" "}
              {policy.data?.policy?.revision ?? "Unavailable"}. Provider
              running, runtime verification and workload health are separate.
            </p>
            <label className="flex items-start gap-2">
              <Checkbox
                checked={ack}
                onCheckedChange={(v) => setAck(v === true)}
              />
              <span>
                I reviewed node costs, unknown additional charges and the
                management reachability requirement.
              </span>
            </label>
          </div>
        )}
        <div className="flex flex-wrap justify-between gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={stage === 0 || busy}
            onClick={() => setStage(stage - 1)}
          >
            Back
          </Button>
          {stage < 3 ? (
            <Button
              size="sm"
              disabled={!valid}
              onClick={() => setStage(stage + 1)}
            >
              Continue
            </Button>
          ) : (
            <Button
              size="sm"
              disabled={!valid || busy || stale}
              onClick={() => void submit()}
            >
              Save review
            </Button>
          )}
        </div>
      </SetupReads>
    </div>
  )
}
