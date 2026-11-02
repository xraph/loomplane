import type { OperationMeta } from '../ops';
import { codec_CapacityUsage } from '../codecs/CapacityUsage';

export const op_cloudCapacity_usage = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/capacity-usage',
  entity: 'CapacityUsage',
  rootType: 'CapacityUsage',
  provides: ['CapacityUsage:{cloudId}'],
  invalidates: [],
  responseCodec: codec_CapacityUsage,
} as const satisfies OperationMeta;
