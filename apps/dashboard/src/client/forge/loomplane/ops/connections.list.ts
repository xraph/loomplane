import type { OperationMeta } from '../ops';
import { codec_ConnectionList } from '../codecs/ConnectionList';

export const op_connections_list = {
  method: 'GET',
  path: '/api/v1/connections',
  entity: 'Connection',
  rootType: 'ConnectionList',
  provides: ['Connection:{id}', 'Connection[]'],
  invalidates: [],
  responseCodec: codec_ConnectionList,
} as const satisfies OperationMeta;
