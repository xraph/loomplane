import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';
import { codec_ReplaceInputBody } from '../codecs/ReplaceInputBody';

export const op_connections_replace = {
  method: 'PUT',
  path: '/api/v1/connections/{id}/credential',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_ReplaceInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
