import { mutation } from '@forge-go/client-core';
import { op_cloudCapacity_savePolicy } from '../ops/cloudCapacity.savePolicy';
import type { CapacityPolicyView } from '../types';

export const useCloudCapacitySavePolicy = /*#__PURE__*/ mutation<CapacityPolicyView, CapacityPolicyView>(op_cloudCapacity_savePolicy);
