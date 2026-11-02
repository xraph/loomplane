import { query } from '@forge-go/client-core';
import { op_cloudCapacity_getPolicy } from '../ops/cloudCapacity.getPolicy';
import type { CapacityPolicyView } from '../types';

export const useCloudCapacityGetPolicy = /*#__PURE__*/ query<CapacityPolicyView>(op_cloudCapacity_getPolicy);
