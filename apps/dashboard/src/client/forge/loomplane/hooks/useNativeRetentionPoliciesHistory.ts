import { query } from '@forge-go/client-core';
import { op_nativeRetentionPolicies_history } from '../ops/nativeRetentionPolicies.history';
import type { RetentionPolicyHistory } from '../types';

export const useNativeRetentionPoliciesHistory = /*#__PURE__*/ query<RetentionPolicyHistory>(op_nativeRetentionPolicies_history);
