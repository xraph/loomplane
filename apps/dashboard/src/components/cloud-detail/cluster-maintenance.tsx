import { useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CloudCluster,
  ClusterInputBody,
  ClusterRetirementReview,
  Profile,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import {
  CloudError,
  Field,
  SetupReads,
} from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { ClusterChoice } from "./cluster-create"
import { ClusterRetirement } from "./cluster-retirement"

export function ClusterMaintenance({
  cluster,
  cloud,
  kind,
  attachmentId,
  attachmentRevision,
  attachmentHpa = false,
  onClose,
  onSaved,
}: {
  cluster: CloudCluster
  cloud: Profile
  kind: string
  attachmentId?: string
  attachmentRevision?: number
  attachmentHpa?: boolean
  onClose: () => void
  onSaved: (id: string) => void
}) {
  const options = useQuery(api.useCloudClustersOptions, {
    path: { cloudId: cloud.id, connectionId: cluster.connectionId },
    query: { region: cluster.observation?.ref.region ?? "" },
  })
  const policy = useQuery(api.useCloudCapacityGetPolicy, {
    path: { cloudId: cloud.id },
  })
  const preview = useMutation(api.useCloudClustersPreview)
  const [poolId, setPoolId] = useState(
    cluster.observation?.pools?.[0]?.id ?? ""
  )
  const pool = cluster.observation?.pools?.find((p) => p.id === poolId)
  const [count, setCount] = useState(String(pool?.count ?? 1))
  const [size, setSize] = useState(pool?.size ?? "")
  const [name, setName] = useState("workers")
  const [version, setVersion] = useState("")
  const [mode, setMode] = useState("manual")
  const [min, setMin] = useState("1")
  const [continuity, setContinuity] = useState("")
  const [max, setMax] = useState("3")
  const [day, setDay] = useState("sunday")
  const [time, setTime] = useState("02:00")
  const [windowStart, setWindowStart] = useState("")
  const [timezone, setTimezone] = useState(
    Intl.DateTimeFormat().resolvedOptions().timeZone
  )
  const [hpa, setHpa] = useState(attachmentHpa)
  const [ack, setAck] = useState(false)
  const [retirement, setRetirement] = useState<ClusterRetirementReview>()
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const locked = useRef(false)
  const retiring = ["retire-cluster", "detach-runtime"].includes(kind)
  const access = kind === "runtime-access-upgrade"
  const destructive =
    kind === "node-pool-delete" ||
    kind === "cluster-upgrade" ||
    (kind === "node-pool-update" && Number(count) < (pool?.count ?? 0))
  const unsupported =
    cluster.provider === "digitalocean" &&
    (destructive || kind === "retire-cluster")
  async function submit() {
    if (locked.current || !options.data || !cluster.observation) return
    locked.current = true
    setBusy(true)
    setError("")
    try {
      const current = await api
        .useCloudClustersGet({
          path: { cloudId: cloud.id, clusterId: cluster.id },
        })
        .refetch()
      const currentCloud = await api
        .useOrganizationCloudsGet({ path: { cloudId: cloud.id } })
        .refetch()
      const freshOptions = await options.refetch()
      const freshPolicy = await policy.refetch()
      if (!freshPolicy.policy)
        throw new Error("Capacity policy is unavailable.")
      const body: ClusterInputBody = {
        kind,
        provider: current.provider,
        accountId: freshOptions.accountId,
        connectionId: current.connectionId,
        resourceId: current.id,
        resourceRevision: current.revision,
        cloudRevision: currentCloud.revision,
        connectionGeneration: freshOptions.connectionGeneration,
        optionsDigest: freshOptions.digest,
        optionsCheckedAt: freshOptions.checkedAt,
        policyRevision: freshPolicy.policy.revision,
        acknowledgeUnknownCosts: ack,
        acknowledgeNodeCosts: ack,
        managementReachable: true,
        workerImage: "",
        ref: current.observation!.ref,
        spec: {
          name: "",
          region: "",
          version: "",
          networkId: "",
          ha: false,
          nodePools: [],
          maintenance: { day: "", startTime: "" },
        },
      }
      if (access) {
        const currentAttachments = await api
          .useCloudClustersAttachments({
            path: { cloudId: cloud.id, clusterId: cluster.id },
          })
          .refetch()
        const selected = currentAttachments.items?.find(
          (item) => item.id === attachmentId
        )
        if (!selected)
          throw new Error(
            "This attachment is no longer active. Choose a current namespace."
          )
        body.runtimeAccess = {
          attachmentId: attachmentId!,
          profile: {
            schemaVersion: 1,
            cleanupDependents: true,
            hpa: hpa || attachmentHpa || selected.profile?.hpa === true,
          },
        }
      } else if (retiring)
        body.retirement = {
          reviewDigest: retirement!.digest,
          ...(kind === "detach-runtime"
            ? { attachmentId, expectedAttachmentRevision: attachmentRevision }
            : {}),
        }
      else {
        body.maintenance = {
          observedRevision: current.observedRevision,
          observationDigest: "",
          poolId: [
            "node-pool-update",
            "node-pool-delete",
            "node-pool-scaling",
          ].includes(kind)
            ? poolId
            : "",
        }
        if (["node-pool-create", "node-pool-update"].includes(kind))
          body.maintenance.pool = {
            ...(kind === "node-pool-update" ? pool : {}),
            id: undefined,
            providerOptions: undefined,
            name: kind === "node-pool-create" ? name : pool!.name,
            size: kind === "node-pool-update" ? pool!.size : size,
            count: Number(count),
            autoscale: false,
            min: 0,
            max: 0,
          }
        if (kind === "cluster-upgrade") body.maintenance.version = version
        if (kind === "cluster-configure")
          body.maintenance.update = {
            fieldMask: ["maintenance"],
            ha: false,
            maintenance: { day, startTime: time },
          }
        if (windowStart)
          body.maintenance.window = {
            localStart: windowStart,
            timezone,
            durationSeconds: 3600,
            startUtc: "0001-01-01T00:00:00Z",
            endUtc: "0001-01-01T00:00:00Z",
          }
        if (kind === "node-pool-scaling")
          body.scaling = {
            poolId,
            mode,
            minNodes: mode === "native" ? Number(min) : 0,
            maxNodes: mode === "native" ? Number(max) : 0,
            continuityFloor: mode === "native" && Number(min) === 0 ? 1 : 0,
            continuityPoolId:
              mode === "native" && Number(min) === 0 ? continuity : undefined,
            cooldownSeconds: 600,
            expectedRevision: Number(
              current.scalingPolicies?.[poolId]?.revision ?? 0
            ),
          }
      }
      const result = await preview.mutateAsync({
        path: { cloudId: cloud.id },
        body,
      })
      onSaved(result.id)
    } catch (e) {
      setError(messageOf(e, "Cluster change could not be reviewed."))
      setStale(statusOf(e) === 409)
      if (statusOf(e) === 409 && retiring) {
        setRetirement(undefined)
        setAck(false)
      }
      if (retiring) setRetirement(undefined)
    } finally {
      locked.current = false
      setBusy(false)
    }
  }
  const valid =
    ack &&
    !unsupported &&
    (!retiring || (!!retirement && !retirement.blockers?.length)) &&
    (!access || !!attachmentId) &&
    (!["node-pool-create", "node-pool-update"].includes(kind) ||
      (!!size && /^\d+$/.test(count) && Number(count) > 0)) &&
    (kind !== "cluster-upgrade" || !!version) &&
    (kind !== "node-pool-scaling" ||
      mode === "manual" ||
      (/^\d+$/.test(min) &&
        Number(min) >= 0 &&
        (Number(min) > 0 || !!continuity) &&
        Number(max) >= 1 &&
        /^\d+$/.test(max) &&
        Number(max) >= Number(min)))
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !busy) onClose()
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Review {kind.replaceAll("-", " ")}</DialogTitle>
          <DialogDescription>
            {cluster.observation?.name || cluster.externalId}. Current
            permission and a fresh review are required for each change.
          </DialogDescription>
        </DialogHeader>
        <CloudError message={error} />
        <SetupReads
          reads={[
            { label: "Provider options", query: options },
            { label: "Capacity policy", query: policy },
          ]}
        >
          {kind.startsWith("node-pool") && kind !== "node-pool-create" && (
            <ClusterChoice
              label="Node pool"
              value={poolId}
              onChange={(id) => {
                setPoolId(id)
                const p = cluster.observation?.pools?.find((p) => p.id === id)
                setCount(String(p?.count ?? 1))
                setSize(p?.size ?? "")
              }}
              items={(cluster.observation?.pools ?? []).map((p) => ({
                value: p.id ?? "",
                label: p.name,
              }))}
            />
          )}
          {["node-pool-create", "node-pool-update"].includes(kind) && (
            <div className="grid gap-3 sm:grid-cols-2">
              {kind === "node-pool-create" && (
                <Field label="Pool name">
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </Field>
              )}
              {kind === "node-pool-create" ? (
                <ClusterChoice
                  label="Node size"
                  value={size}
                  onChange={setSize}
                  items={(options.data?.nodeSizes ?? []).map((v) => ({
                    value: v,
                    label: v,
                  }))}
                />
              ) : (
                <p className="self-center text-sm">
                  Node size: {pool?.size}. Count changes preserve the existing
                  pool shape.
                </p>
              )}
              <Field label="Desired nodes">
                <Input
                  type="number"
                  min={1}
                  value={count}
                  onChange={(e) => setCount(e.target.value)}
                />
              </Field>
            </div>
          )}
          {kind === "node-pool-scaling" && (
            <>
              <ClusterChoice
                label="Count controller"
                value={mode}
                onChange={setMode}
                items={[
                  { value: "manual", label: "Loomplane manual changes" },
                  { value: "native", label: "Provider native autoscaler" },
                ]}
              />
              {mode === "native" && (
                <div className="grid grid-cols-2 gap-3">
                  <Field label="Minimum nodes">
                    <Input
                      type="number"
                      min={0}
                      value={min}
                      onChange={(e) => setMin(e.target.value)}
                    />
                  </Field>
                  <Field label="Maximum nodes">
                    <Input
                      type="number"
                      min={min}
                      value={max}
                      onChange={(e) => setMax(e.target.value)}
                    />
                  </Field>
                </div>
              )}
              {mode === "native" && Number(min) === 0 && (
                <ClusterChoice
                  label="Continuity pool (at least one fixed node)"
                  value={continuity}
                  onChange={setContinuity}
                  items={(cluster.observation?.pools ?? [])
                    .filter(
                      (p) => p.id !== poolId && !p.autoscale && p.count >= 1
                    )
                    .map((p) => ({ value: p.id ?? "", label: p.name }))}
                />
              )}
              <p className="text-xs">
                The provider can add and remove nodes within reviewed native
                bounds. Loomplane's ten-minute growth cooldown applies to its
                own changes, not provider additions. Native growth requires
                current known prices and quota; unknown charges cannot be
                acknowledged away.
              </p>
            </>
          )}
          {kind === "cluster-upgrade" && (
            <ClusterChoice
              label="Kubernetes version"
              value={version}
              onChange={setVersion}
              items={(options.data?.versions ?? []).map((v) => ({
                value: v,
                label: v,
              }))}
            />
          )}
          {kind === "cluster-configure" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <ClusterChoice
                label="Maintenance day"
                value={day}
                onChange={setDay}
                items={[
                  "monday",
                  "tuesday",
                  "wednesday",
                  "thursday",
                  "friday",
                  "saturday",
                  "sunday",
                ].map((v) => ({ value: v, label: v }))}
              />
              <Field label="Provider start time (UTC)">
                <Input
                  type="time"
                  value={time}
                  onChange={(e) => setTime(e.target.value)}
                />
              </Field>
            </div>
          )}
          {destructive && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="One-hour maintenance window">
                <Input
                  type="datetime-local"
                  value={windowStart}
                  onChange={(e) => setWindowStart(e.target.value)}
                />
              </Field>
              <Field label="Timezone">
                <Input
                  value={timezone}
                  onChange={(e) => setTimezone(e.target.value)}
                />
              </Field>
            </div>
          )}
          {unsupported && (
            <p role="status" className="text-sm">
              DigitalOcean has no qualified no-force path for this removal or
              upgrade. Complete ownership of cluster children is also required
              for deletion. You cannot bypass this restriction.
            </p>
          )}
          {access && (
            <div className="grid gap-2 text-sm">
              <p>Review dependent cleanup permissions for this namespace.</p>
              <label className="flex items-start gap-2">
                <Checkbox
                  aria-label="Allow horizontal scaling"
                  checked={hpa || attachmentHpa}
                  disabled={attachmentHpa}
                  onCheckedChange={(v) => setHpa(v === true)}
                />
                Allow horizontal scaling
                {attachmentHpa ? " (already enabled)" : " (optional)"}
              </label>
              <p className="text-xs text-muted-foreground">
                Existing permissions stay enabled. This review does not renew
                credentials or change cluster trust.
              </p>
            </div>
          )}
          {retiring && (
            <ClusterRetirement
              cluster={cluster}
              attachmentId={
                kind === "detach-runtime" ? attachmentId : undefined
              }
              attachmentRevision={attachmentRevision}
              onReview={(review) => {
                setAck(false)
                setRetirement(review)
              }}
            />
          )}
          <label className="flex items-start gap-2 text-sm">
            <Checkbox
              aria-label={
                access
                  ? "Review the requested namespace permissions"
                  : "Acknowledge review"
              }
              checked={ack}
              onCheckedChange={(v) => setAck(v === true)}
            />
            <span>
              {retiring
                ? "You have reviewed retained resources and any remaining charges."
                : access
                  ? "Review the requested namespace permissions for this attachment."
                  : kind === "node-pool-scaling" && mode === "native"
                    ? "Review provider-managed bounds. Required prices and quota must be known before applying."
                    : "Prepare a manual cost review, including unknown charges. You will see the scoped categories before applying."}
            </span>
          </label>
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={onClose}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => void submit()}
              disabled={!valid || busy}
            >
              {busy
                ? "Reviewing..."
                : stale
                  ? "Create fresh review"
                  : "Save review"}
            </Button>
          </div>
        </SetupReads>
      </DialogContent>
    </Dialog>
  )
}
