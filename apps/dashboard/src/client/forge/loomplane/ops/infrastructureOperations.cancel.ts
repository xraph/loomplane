import type { OperationMeta } from '../ops';
import { codec_Operation } from '../codecs/Operation';

export const op_infrastructureOperations_cancel = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/infrastructure-operations/{operationId}/cancel',
  entity: 'Operation',
  rootType: 'Operation',
  provides: ['Operation:{id}'],
  invalidates: ['Operation[]'],
  responseCodec: codec_Operation,
} as const satisfies OperationMeta;
