import { query } from '@forge-go/client-core';
import { op_cloudCapacity_usage } from '../ops/cloudCapacity.usage';
import type { CapacityUsage } from '../types';

export const useCloudCapacityUsage = /*#__PURE__*/ query<CapacityUsage>(op_cloudCapacity_usage);
