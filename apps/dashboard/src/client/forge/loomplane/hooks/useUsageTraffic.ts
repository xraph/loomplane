import { query } from '@forge-go/client-core';
import { op_usage_traffic } from '../ops/usage.traffic';
import type { TrafficReport } from '../types';

export const useUsageTraffic = /*#__PURE__*/ query<TrafficReport>(op_usage_traffic);
