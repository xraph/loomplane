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
  Cell,
  CloudTable,
  CloudTableRow,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import { CloudError } from "@/components/cloud-targets/infrastructure-ui"
import { StatusPill } from "@/components/runs"
import { Link } from "@/lib/router"
import { microCost, observedTime } from "./cluster-money"
import { ownershipPhrase } from "./clusters"

const resourceColumns =
  "minmax(0,1.3fr) minmax(0,1fr) minmax(0,1.1fr) minmax(0,1.2fr)"

/**
 * ClusterRetirement reads what retiring a cluster or detaching a namespace
 * deletes and what it leaves running, before the review is saved.
 */
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
      setError(messageOf(e, "Retirement inventory couldn't be reviewed."))
    }
  }
  const resources = review
    ? [
        ...new Map(
          [
            ...(review.deletionCandidates ?? []),
            ...(review.retainedResources ?? []),
          ].map((r) => [r.id, r])
        ).values(),
      ]
    : []
  return (
    <div className="grid gap-2.5">
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
          <p className="text-[13px]">
            Remaining known charges:{" "}
            {microCost(review.estimatedRemainingHourlyMicroUsd)}. Checked{" "}
            {observedTime(review.checkedAt) ?? "at an unknown time"}.
          </p>
          {(review.blockers ?? []).map((b) => (
            <p key={b} role="status" className="text-[13px]">
              {b.startsWith("connection_use:plan:") ? (
                <>
                  A deployment plan still uses this connection.{" "}
                  <Link
                    className="text-foreground underline underline-offset-2"
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
          {resources.length > 0 && (
            <CloudTable
              label="Retirement inventory"
              columns={resourceColumns}
              head={["Resource", "What happens", "Ownership", "Cost"]}
              minWidth={560}
            >
              {resources.map((r) => (
                <CloudTableRow key={r.id}>
                  <NameCell mono name={r.id} meta={r.kind} />
                  <Cell>
                    {r.survivesClusterDeletion ? (
                      <StatusPill tone="waiting">Retained</StatusPill>
                    ) : (
                      <StatusPill tone="failed">Deleted</StatusPill>
                    )}
                  </Cell>
                  <Cell className="text-[12.5px]">
                    {ownershipPhrase(r.ownership)}
                  </Cell>
                  <Cell className="text-[12.5px] tabular-nums">
                    {microCost(r.estimatedHourlyMicroUsd)}
                  </Cell>
                </CloudTableRow>
              ))}
            </CloudTable>
          )}
          {!!review.unknownCosts?.length && (
            <p className="text-[12.5px] text-muted-foreground">
              Some retained charges are unknown. Review does not delete
              resources or release capacity.
            </p>
          )}
        </>
      )}
    </div>
  )
}
