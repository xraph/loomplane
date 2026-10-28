import type { OperationMeta } from '../ops';
import { codec_CapacityUsage } from '../codecs/CapacityUsage';

export const op_cloudCapacity_refresh = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/capacity-usage/refresh',
  entity: 'CapacityUsage',
  rootType: 'CapacityUsage',
  provides: ['CapacityUsage:{cloudId}'],
  invalidates: ['CapacityUsage[]'],
  responseCodec: codec_CapacityUsage,
} as const satisfies OperationMeta;
