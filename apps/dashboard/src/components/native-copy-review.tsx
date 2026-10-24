import { useRef, useState } from "react"

import { useMutation, useQuery } from "@/client/hooks"
import {
  useCloudsList,
  useCloudTargetsList,
  useNativeRecoveryCopiesReview,
  useNativeRecoveryCopiesCreate,
} from "@/client/forge/loomplane/hooks"
import type {
  NativeSource,
  NativePoint,
  NativeCopyReview,
  Profile,
  Target,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useOrganization } from "@/lib/organization"
import { useNow } from "@/lib/use-runs"
import { formatDate } from "@/lib/format"
import { NativeEmpty } from "@/components/native-recovery-empty"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Checkbox } from "@/components/ui/checkbox"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export function NativeCopyButton({
  source,
  point,
  onAccepted,
  disabled,
}: {
  source: NativeSource
  point: NativePoint
  onAccepted: () => void
  disabled?: boolean
}) {
  const { can } = useOrganization()
  const [open, setOpen] = useState(false)
  if (
    !can("service_restore:manage") ||
    !["cloud-sql", "rds"].includes(source.provider) ||
    point.engine !== "postgres" ||
    (point.version !== "17" &&
      !(source.provider === "rds" && point.version.startsWith("17."))) ||
    !point.present ||
    !point.encrypted ||
    point.status !== (source.provider === "rds" ? "available" : "SUCCESSFUL")
  )
    return null
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        disabled={disabled}
        onClick={() => setOpen(true)}
        aria-label={`Review native copy from ${point.name}`}
      >
        Review native copy
      </Button>
      {open && (
        <CopyReview
          source={source}
          point={point}
          onAccepted={onAccepted}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

function CopyReview({
  source,
  point,
  onAccepted,
  onClose,
}: {
  source: NativeSource
  point: NativePoint
  onAccepted: () => void
  onClose: () => void
}) {
  const { can } = useOrganization()
  const clouds = useQuery(useCloudsList, { path: { id: source.projectId } })
  const [chosenCloud, setChosenCloud] = useState("")
  const [target, setTarget] = useState("")
  const [hours, setHours] = useState("1")
  const [review, setReview] = useState<NativeCopyReview | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [failure, setFailure] = useState("")
  const request = useRef<string | null>(null)
  const reviewMutation = useMutation(useNativeRecoveryCopiesReview)
  const create = useMutation(useNativeRecoveryCopiesCreate)
  const now = useNow()
  const list = (clouds.data?.clouds ?? []).filter(
    (cloud): cloud is Profile => !!cloud
  )
  const cloud = list.find((item) => item.id === chosenCloud)
  const ttl = Number(hours)
  const valid =
    !!cloud && !!target && Number.isInteger(ttl) && ttl >= 1 && ttl <= 24
  const owner = can("service_restore:manage")
  const busy = reviewMutation.isPending || create.isPending
  const expired = !!review && new Date(review.validUntil).getTime() <= now
  function invalidate() {
    request.current = null
    setReview(null)
    setAcknowledged(false)
    setFailure("")
  }
  async function loadReview() {
    if (!valid || busy || submitted || !owner) return
    const requestId = (request.current ??= crypto.randomUUID())
    setFailure("")
    setReview(null)
    setAcknowledged(false)
    try {
      const result = (await reviewMutation.mutateAsync({
        path: { id: source.id, pointId: point.id },
        body: {
          cloudId: cloud!.id,
          targetId: target,
          ttlHours: ttl,
          requestId,
        },
      })) as NativeCopyReview
      if (request.current === requestId) setReview(result)
    } catch (cause) {
      setFailure(
        messageOf(
          cause,
          "Could not review this target. Check the project grant and target, then retry."
        )
      )
    }
  }
  async function admit() {
    if (!review || !owner || !acknowledged || busy || (expired && !submitted))
      return
    setSubmitted(true)
    setFailure("")
    try {
      await create.mutateAsync({
        body: {
          requestId: review.requestId,
          reviewToken: review.reviewToken,
          acknowledgeCharges: true,
        },
      })
      onAccepted()
      onClose()
    } catch (cause) {
      setFailure(
        `${messageOf(cause, "The admission response was not confirmed.")} Retry the same request, or close and check native copies. A copy may already exist.`
      )
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
    >
      <DialogContent className="sm:max-w-lg" showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>Review native copy</DialogTitle>
          <DialogDescription className="break-words">
            Copy {point.name} from {source.name} into{" "}
            {source.provider === "rds"
              ? "a new private RDS instance"
              : "a separate recovery project"}
            . The source database stays unchanged.
          </DialogDescription>
        </DialogHeader>
        {!owner && (
          <Alert variant="destructive">
            <AlertDescription>
              You no longer have permission to create a recovery copy.
            </AlertDescription>
          </Alert>
        )}
        {clouds.status === "error" ? (
          <QueryError
            error={clouds.error}
            fallback="Could not load granted Clouds"
            retry={clouds.refetch}
          />
        ) : !clouds.data ? (
          <p role="status">Loading granted Clouds…</p>
        ) : !list.length ? (
          <NativeEmpty title="No granted Cloud targets">
            Ask your organization manager to grant this project a Cloud with a
            {source.provider === "rds"
              ? "managed RDS target with a private recovery route in the source account and region."
              : "managed Cloud SQL target in a separate recovery project."}
          </NativeEmpty>
        ) : (
          <>
            <div className="grid min-w-0 gap-3 sm:grid-cols-2">
              <div className="grid gap-1">
                <Label htmlFor="native-copy-cloud">Destination Cloud</Label>
                <Select
                  value={chosenCloud}
                  disabled={busy || submitted}
                  onValueChange={(value) => {
                    setChosenCloud(value ?? "")
                    setTarget("")
                    invalidate()
                  }}
                >
                  <SelectTrigger
                    id="native-copy-cloud"
                    aria-label="Destination Cloud"
                    className="w-full"
                  >
                    <SelectValue placeholder="Choose a Cloud" />
                  </SelectTrigger>
                  <SelectContent>
                    {list.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-1">
                <Label htmlFor="native-copy-hours">Lifetime in hours</Label>
                <Input
                  id="native-copy-hours"
                  type="number"
                  min={1}
                  max={24}
                  step={1}
                  value={hours}
                  disabled={busy || submitted}
                  onChange={(event) => {
                    setHours(event.target.value)
                    invalidate()
                  }}
                />
              </div>
            </div>
            {cloud && (
              <TargetChoice
                key={cloud.id}
                source={source}
                cloud={cloud}
                value={target}
                disabled={busy || submitted}
                onChange={(value) => {
                  setTarget(value)
                  invalidate()
                }}
              />
            )}
            {!submitted && (
              <Button
                size="sm"
                variant="outline"
                disabled={
                  !owner || !valid || busy || clouds.status !== "success"
                }
                onClick={() => void loadReview()}
              >
                {reviewMutation.isPending
                  ? "Checking capacity…"
                  : "Review capacity and charges"}
              </Button>
            )}
          </>
        )}
        {review && (
          <div className="grid min-w-0 gap-2 rounded-md bg-muted/50 p-3">
            <p className="text-sm font-medium break-all">
              {review.target.nativeProject ||
                review.target.nativeAccount ||
                review.target.provider}{" "}
              · {review.target.region}
            </p>
            <p className="text-xs text-muted-foreground">
              {review.capacity.computeClass} · {review.capacity.storageGb} GiB{" "}
              {review.capacity.storageType} · {review.capacity.availability} ·{" "}
              {review.ttlHours} hour lifetime
            </p>
            <p className="text-xs">{review.notice}</p>
            <p className="text-xs text-muted-foreground">
              Review valid until {formatDate(review.validUntil)}. Provider
              charges are not a price estimate.
            </p>
            {expired && !submitted && (
              <p role="status" className="text-xs text-destructive">
                This review expired. Review capacity and charges again.
              </p>
            )}
            <Label className="flex items-start gap-2">
              <Checkbox
                checked={acknowledged}
                disabled={busy || !owner || (expired && !submitted)}
                onCheckedChange={(value) => setAcknowledged(value === true)}
              />
              <span>I acknowledge charges until deletion is confirmed.</span>
            </Label>
            <Button
              size="sm"
              disabled={
                !owner || !acknowledged || busy || (expired && !submitted)
              }
              onClick={() => void admit()}
            >
              {create.isPending
                ? "Submitting copy…"
                : submitted
                  ? "Retry same request"
                  : "Create temporary copy"}
            </Button>
            <p className="text-xs break-all text-muted-foreground">
              Request {review.requestId}
            </p>
          </div>
        )}
        {failure && (
          <Alert variant="destructive">
            <AlertDescription>{failure}</AlertDescription>
          </Alert>
        )}
      </DialogContent>
    </Dialog>
  )
}

function TargetChoice({
  source,
  cloud,
  value,
  disabled,
  onChange,
}: {
  source: NativeSource
  cloud: Profile
  value: string
  disabled: boolean
  onChange: (value: string) => void
}) {
  const targets = useQuery(useCloudTargetsList, {
    path: { id: source.projectId, cloudId: cloud.id },
  })
  const choices = (targets.data?.targets ?? []).filter(
    (target): target is Target =>
      !!target &&
      target.provider === source.provider &&
      target.runtime === "managed" &&
      !target.poolId &&
      (source.provider === "rds"
        ? target.region === source.region
        : target.connectionId !== source.connectionId)
  )
  if (targets.status === "error")
    return (
      <QueryError
        error={targets.error}
        fallback="Could not load granted targets"
        retry={targets.refetch}
      />
    )
  if (!targets.data) return <p role="status">Loading granted targets…</p>
  if (!choices.length)
    return (
      <NativeEmpty title="No compatible recovery target">
        Choose a Cloud with a compatible managed recovery target. The server
        checks the account, region and current grant before admission.
      </NativeEmpty>
    )
  return (
    <div className="grid gap-1">
      <Label htmlFor="native-copy-target">Destination target</Label>
      <Select
        value={value}
        disabled={disabled || targets.status !== "success"}
        onValueChange={(next) => onChange(next ?? "")}
      >
        <SelectTrigger
          id="native-copy-target"
          aria-label="Destination target"
          className="w-full"
        >
          <SelectValue placeholder="Choose a recovery target" />
        </SelectTrigger>
        <SelectContent>
          {choices.map((target) => (
            <SelectItem key={target.id} value={target.id}>
              {target.displayName || target.name} · {target.region}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
