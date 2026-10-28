import type { OperationMeta } from '../ops';
import { codec_CapacityPolicyInputBody } from '../codecs/CapacityPolicyInputBody';
import { codec_CapacityPolicyView } from '../codecs/CapacityPolicyView';

export const op_cloudCapacity_savePolicy = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/capacity-policy',
  entity: 'CapacityPolicyView',
  rootType: 'CapacityPolicyView',
  provides: ['CapacityPolicyView:{cloudId}'],
  invalidates: ['CapacityPolicyView[]', 'CapacityUsage:{cloudId}'],
  bodyCodec: codec_CapacityPolicyInputBody,
  responseCodec: codec_CapacityPolicyView,
} as const satisfies OperationMeta;
