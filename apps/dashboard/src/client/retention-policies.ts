import { query } from "@forge-go/client-core"

import { op_retentionPolicies_get } from "@/client/forge/loomplane/ops/retentionPolicies.get"
import { op_retentionPolicies_history } from "@/client/forge/loomplane/ops/retentionPolicies.history"
import type {
  RetentionPolicyHistory,
  RetentionPolicyState,
} from "@/client/forge/loomplane/types"

// Forge 1.12.9 derives no read tag for an unset policy or revision history.
// These bindings add their shared dependency using client-core's query factory.
// A save through any backup alias must refresh both, including empty results.
const provides = ["RetentionPolicy[]"] as const
export const useRetentionPoliciesGet = query<RetentionPolicyState>({
  ...op_retentionPolicies_get,
  provides: [...op_retentionPolicies_get.provides, ...provides],
})
export const useRetentionPoliciesHistory = query<RetentionPolicyHistory>({
  ...op_retentionPolicies_history,
  provides: [...op_retentionPolicies_history.provides, ...provides],
})
export { useRetentionPoliciesSet } from "@/client/forge/loomplane/hooks/useRetentionPoliciesSet"
