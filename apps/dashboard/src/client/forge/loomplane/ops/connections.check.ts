import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';

export const op_connections_check = {
  method: 'POST',
  path: '/api/v1/connections/{id}/check',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
