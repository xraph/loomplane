import type { OperationMeta } from '../ops';
import { codec_ApplyInputBody } from '../codecs/ApplyInputBody';
import { codec_Operation } from '../codecs/Operation';

export const op_infrastructureOperations_apply = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/infrastructure-operations/{operationId}/apply',
  entity: 'Operation',
  rootType: 'Operation',
  provides: ['Operation:{id}'],
  invalidates: ['Operation[]'],
  bodyCodec: codec_ApplyInputBody,
  responseCodec: codec_Operation,
} as const satisfies OperationMeta;
