import { query } from "@forge-go/client-core"
import { op_nativeRetentionPolicies_get } from "@/client/forge/loomplane/ops/nativeRetentionPolicies.get"
import { op_nativeRetentionPolicies_history } from "@/client/forge/loomplane/ops/nativeRetentionPolicies.history"
import type {
  NativeRetentionPolicyState,
  RetentionPolicyHistory,
} from "@/client/forge/loomplane/types"

// Unset policies and empty histories still depend on the next policy save.
const provides = ["NativeRetentionPolicy[]"]
export const useNativeRetentionPoliciesGet = query<NativeRetentionPolicyState>({
  ...op_nativeRetentionPolicies_get,
  provides: [...op_nativeRetentionPolicies_get.provides, ...provides],
})
export const useNativeRetentionPoliciesHistory = query<RetentionPolicyHistory>({
  ...op_nativeRetentionPolicies_history,
  provides: [...op_nativeRetentionPolicies_history.provides, ...provides],
})
export { useNativeRetentionPoliciesSet } from "@/client/forge/loomplane/hooks/useNativeRetentionPoliciesSet"
