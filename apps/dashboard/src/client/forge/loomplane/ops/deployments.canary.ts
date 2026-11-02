import type { OperationMeta } from '../ops';
import { codec_CanaryReport } from '../codecs/CanaryReport';

export const op_deployments_canary = {
  method: 'GET',
  path: '/api/v1/deployments/{id}/canary',
  entity: 'CanaryVerdict',
  rootType: 'CanaryReport',
  provides: ['CanaryVerdict:{id}', 'CanaryVerdict[]'],
  invalidates: [],
  responseCodec: codec_CanaryReport,
} as const satisfies OperationMeta;
