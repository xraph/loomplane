import { query } from '@forge-go/client-core';
import { op_retentionPolicies_history } from '../ops/retentionPolicies.history';
import type { RetentionPolicyHistory } from '../types';

export const useRetentionPoliciesHistory = /*#__PURE__*/ query<RetentionPolicyHistory>(op_retentionPolicies_history);
