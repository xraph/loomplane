import type { OperationMeta } from '../ops';
import { codec_ExpansionList } from '../codecs/ExpansionList';

export const op_cloudCapacity_listExpansions = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/expansions',
  provides: [],
  invalidates: [],
  responseCodec: codec_ExpansionList,
} as const satisfies OperationMeta;
