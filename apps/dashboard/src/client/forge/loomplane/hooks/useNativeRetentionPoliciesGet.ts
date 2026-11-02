import { query } from '@forge-go/client-core';
import { op_nativeRetentionPolicies_get } from '../ops/nativeRetentionPolicies.get';
import type { NativeRetentionPolicyState } from '../types';

export const useNativeRetentionPoliciesGet = /*#__PURE__*/ query<NativeRetentionPolicyState>(op_nativeRetentionPolicies_get);
