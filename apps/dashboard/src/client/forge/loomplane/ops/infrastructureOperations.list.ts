import type { OperationMeta } from '../ops';
import { codec_OperationList } from '../codecs/OperationList';

export const op_infrastructureOperations_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/infrastructure-operations',
  provides: [],
  invalidates: [],
  responseCodec: codec_OperationList,
} as const satisfies OperationMeta;
