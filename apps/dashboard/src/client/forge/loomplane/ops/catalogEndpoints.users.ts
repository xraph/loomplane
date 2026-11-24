import type { OperationMeta } from '../ops';
import { codec_OwnUserList } from '../codecs/OwnUserList';

export const op_catalogEndpoints_users = {
  method: 'GET',
  path: '/api/v1/catalog/endpoints/{id}/users',
  provides: [],
  invalidates: [],
  responseCodec: codec_OwnUserList,
} as const satisfies OperationMeta;
