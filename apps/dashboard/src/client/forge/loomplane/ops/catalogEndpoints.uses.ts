import type { OperationMeta } from '../ops';
import { codec_ServiceUseList } from '../codecs/ServiceUseList';

export const op_catalogEndpoints_uses = {
  method: 'GET',
  path: '/api/v1/catalog/endpoints/{id}/uses',
  provides: [],
  invalidates: [],
  responseCodec: codec_ServiceUseList,
} as const satisfies OperationMeta;
