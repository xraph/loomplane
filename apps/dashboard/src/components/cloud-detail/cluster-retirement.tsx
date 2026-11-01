import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CloudCluster,
  ClusterRetirementReview,
} from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import {
  CloudError,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"
import { Link } from "@/lib/router"
import { exactUSD, observedTime } from "./cluster-money"
export function ClusterRetirement({
  cluster,
  attachmentId,
  attachmentRevision,
  onReview,
}: {
  cluster: CloudCluster
  attachmentId?: string
  attachmentRevision?: number
  onReview: (review: ClusterRetirementReview | undefined) => void
}) {
  const preview = useMutation(api.useCloudClustersRetirementPreview)
  const [review, setReview] = useState<ClusterRetirementReview>()
  const [error, setError] = useState("")
  async function refresh() {
    setError("")
    onReview(undefined)
    try {
      const result = (await preview.mutateAsync({
        path: { cloudId: cluster.cloudId, clusterId: cluster.id },
        body: {
          attachmentId,
          expectedAttachmentRevision: attachmentId
            ? attachmentRevision
            : undefined,
        },
      })) as ClusterRetirementReview
      setReview(result)
      onReview(result)
    } catch (e) {
      setError(messageOf(e, "Retirement inventory could not be reviewed."))
    }
  }
  return (
    <div className="grid gap-2">
      <Button
        size="sm"
        variant="outline"
        className="justify-self-start"
        disabled={preview.isPending}
        onClick={() => void refresh()}
      >
        Review retained resources
      </Button>
      <CloudError message={error} />
      {review && (
        <>
          <p className="text-sm">
            Remaining known charges:{" "}
            {exactUSD(review.estimatedRemainingHourlyMicroUsd)} / hour. Checked{" "}
            {observedTime(review.checkedAt) ?? "time unavailable"}.
          </p>
          {(review.blockers ?? []).map((b) => (
            <p key={b} role="status" className="text-sm">
              {b.startsWith("connection_use:plan:") ? (
                <>
                  A deployment plan still uses this connection.{" "}
                  <Link
                    className="text-primary underline"
                    to="/deployments/$id"
                    params={{ id: b.slice("connection_use:plan:".length) }}
                  >
                    Review deployment
                  </Link>
                </>
              ) : b.startsWith("connection_use:") ? (
                `Another ${b.split(":")[1].replaceAll("_", " ")} still uses this connection.`
              ) : (
                b.replaceAll("_", " ").replaceAll(":", " · ")
              )}
            </p>
          ))}
          <Rows>
            {[
              ...new Map(
                [
                  ...(review.deletionCandidates ?? []),
                  ...(review.retainedResources ?? []),
                ].map((r) => [r.id, r])
              ).values(),
            ].map((r) => (
              <Row key={r.id}>
                <span className="break-all">
                  {r.kind} · {r.id}
                </span>
                <span>
                  {r.survivesClusterDeletion
                    ? "Retained"
                    : "Deletion candidate"}{" "}
                  · {r.ownership} · {exactUSD(r.estimatedHourlyMicroUsd)} / hour
                </span>
              </Row>
            ))}
          </Rows>
          {!!review.unknownCosts?.length && (
            <p className="text-xs">
              Some retained charges are unknown. Review does not delete
              resources or release capacity.
            </p>
          )}
        </>
      )}
    </div>
  )
}
