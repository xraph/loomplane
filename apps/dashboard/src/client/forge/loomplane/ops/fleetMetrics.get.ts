import type { OperationMeta } from '../ops';
import { codec_MetricSummary } from '../codecs/MetricSummary';

export const op_fleetMetrics_get = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/instances/{id}/metrics',
  provides: [],
  invalidates: [],
  responseCodec: codec_MetricSummary,
} as const satisfies OperationMeta;
