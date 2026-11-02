import { query } from "@forge-go/client-core"

import { op_deployments_canary } from "@/client/forge/loomplane/ops/deployments.canary"
import type { CanaryReport } from "@/client/forge/loomplane/types"

// The generated route provides only verdict entities, but the stream
// invalidates the CanaryReport tag when a verdict lands, and a status change
// with no new verdict (a rollback that couldn't finish, the next weight, a
// promotion) arrives as deployment.updated. Providing both tags keeps the
// report current on either event.
const provides = ["CanaryReport", "Deployment[]"]
export const useDeploymentsCanary = query<CanaryReport>({
  ...op_deployments_canary,
  provides: [...op_deployments_canary.provides, ...provides],
})
