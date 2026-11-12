import { query } from '@forge-go/client-core';
import { op_fleetMetrics_get } from '../ops/fleetMetrics.get';
import type { MetricSummary } from '../types';

export const useFleetMetricsGet = /*#__PURE__*/ query<MetricSummary>(op_fleetMetrics_get);
