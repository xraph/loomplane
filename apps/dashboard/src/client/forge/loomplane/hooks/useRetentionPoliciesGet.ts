import { query } from '@forge-go/client-core';
import { op_retentionPolicies_get } from '../ops/retentionPolicies.get';
import type { RetentionPolicyState } from '../types';

export const useRetentionPoliciesGet = /*#__PURE__*/ query<RetentionPolicyState>(op_retentionPolicies_get);
