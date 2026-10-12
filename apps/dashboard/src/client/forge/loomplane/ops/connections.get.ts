import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';

export const op_connections_get = {
  method: 'GET',
  path: '/api/v1/connections/{id}',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: [],
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
