import { mutation } from '@forge-go/client-core';
import { op_cloudCapacity_approveExpansion } from '../ops/cloudCapacity.approveExpansion';
import type { Expansion } from '../types';

export const useCloudCapacityApproveExpansion = /*#__PURE__*/ mutation<Expansion, Expansion>(op_cloudCapacity_approveExpansion);
