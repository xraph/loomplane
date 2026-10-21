import type { OperationMeta } from '../ops';
import { codec_Operation } from '../codecs/Operation';

export const op_infrastructureOperations_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/infrastructure-operations/{operationId}',
  entity: 'Operation',
  rootType: 'Operation',
  provides: ['Operation:{id}'],
  invalidates: [],
  responseCodec: codec_Operation,
} as const satisfies OperationMeta;
