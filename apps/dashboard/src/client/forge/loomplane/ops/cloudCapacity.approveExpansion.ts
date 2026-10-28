import type { OperationMeta } from '../ops';
import { codec_ApproveExpansionInputBody } from '../codecs/ApproveExpansionInputBody';
import { codec_Expansion } from '../codecs/Expansion';

export const op_cloudCapacity_approveExpansion = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/expansions/{expansionId}/approve',
  entity: 'Expansion',
  rootType: 'Expansion',
  provides: ['Expansion:{id}'],
  invalidates: ['CapacityUsage:{cloudId}', 'Expansion[]'],
  bodyCodec: codec_ApproveExpansionInputBody,
  responseCodec: codec_Expansion,
} as const satisfies OperationMeta;
