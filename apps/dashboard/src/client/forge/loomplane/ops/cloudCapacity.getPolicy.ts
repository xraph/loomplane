import type { OperationMeta } from '../ops';
import { codec_CapacityPolicyView } from '../codecs/CapacityPolicyView';

export const op_cloudCapacity_getPolicy = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/capacity-policy',
  provides: [],
  invalidates: [],
  responseCodec: codec_CapacityPolicyView,
} as const satisfies OperationMeta;
