import type { OperationMeta } from '../ops';
import { codec_CloudTargetList } from '../codecs/CloudTargetList';

export const op_cloudTargets_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudTargetList,
} as const satisfies OperationMeta;
