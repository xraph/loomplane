import { query } from "@forge-go/client-core"

import { op_deployments_canary } from "@/client/forge/loomplane/ops/deployments.canary"
import type { CanaryReport } from "@/client/forge/loomplane/types"
import { deploymentLists } from "@/client/tags"

// The generated route provides the verdicts, which a canary_verdict event
// reaches. A status change with no new verdict (a rollback that couldn't
// finish, the next weight, a promotion) arrives as deployment.updated, which
// invalidates the deployment lists, so the report carries that tag too.
const provides = [deploymentLists]
export const useDeploymentsCanary = query<CanaryReport>({
  ...op_deployments_canary,
  provides: [...op_deployments_canary.provides, ...provides],
})
