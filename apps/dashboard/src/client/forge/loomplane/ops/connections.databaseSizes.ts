import type { OperationMeta } from '../ops';
import { codec_DatabaseSizes } from '../codecs/DatabaseSizes';

export const op_connections_databaseSizes = {
  method: 'GET',
  path: '/api/v1/connections/{id}/database-sizes/{kind}/{region}',
  provides: [],
  invalidates: [],
  responseCodec: codec_DatabaseSizes,
} as const satisfies OperationMeta;
