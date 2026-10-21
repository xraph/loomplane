import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';
import { codec_ConnectionsCreateInputBody } from '../codecs/ConnectionsCreateInputBody';

export const op_connections_create = {
  method: 'POST',
  path: '/api/v1/connections',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_ConnectionsCreateInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
