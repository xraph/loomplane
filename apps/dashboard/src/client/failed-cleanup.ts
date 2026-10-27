import { mutation, query } from "@forge-go/client-core"
import { op_deployments_cleanup } from "@/client/forge/loomplane/ops/deployments.cleanup"
import { op_deployments_cleanupPreview } from "@/client/forge/loomplane/ops/deployments.cleanupPreview"
import { op_deployments_cleanupApply } from "@/client/forge/loomplane/ops/deployments.cleanupApply"
import { op_deployments_cleanupRetry } from "@/client/forge/loomplane/ops/deployments.cleanupRetry"
import type {
  CleanupReview,
  FailedCleanup,
} from "@/client/forge/loomplane/types"

export type { CleanupReview } from "@/client/forge/loomplane/types"
// An absent cleanup still depends on its next acceptance. The operation is
// keyed by deployment rather than by a serving resource entity.
const provides = ["FailedCleanup:{id}"]
const invalidates = [...provides, "Deployment:{id}", "Deployment[]"]
export const useFailedCleanup = query<FailedCleanup>({
  ...op_deployments_cleanup,
  provides,
})
export const useCleanupPreview = mutation<CleanupReview>(
  op_deployments_cleanupPreview
)
export const useCleanupApply = mutation<FailedCleanup, { revision: string }>({
  ...op_deployments_cleanupApply,
  invalidates,
})
export const useCleanupRetry = mutation<FailedCleanup>({
  ...op_deployments_cleanupRetry,
  invalidates,
})
