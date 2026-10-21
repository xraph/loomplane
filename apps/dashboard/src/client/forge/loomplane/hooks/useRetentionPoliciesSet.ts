import { mutation } from '@forge-go/client-core';
import { op_retentionPolicies_set } from '../ops/retentionPolicies.set';
import type { RetentionPolicy } from '../types';

export const useRetentionPoliciesSet = /*#__PURE__*/ mutation<RetentionPolicy, RetentionPolicy>(op_retentionPolicies_set);
