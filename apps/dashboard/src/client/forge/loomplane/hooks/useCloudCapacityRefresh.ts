import { mutation } from '@forge-go/client-core';
import { op_cloudCapacity_refresh } from '../ops/cloudCapacity.refresh';
import type { CapacityUsage } from '../types';

export const useCloudCapacityRefresh = /*#__PURE__*/ mutation<CapacityUsage, CapacityUsage>(op_cloudCapacity_refresh);
