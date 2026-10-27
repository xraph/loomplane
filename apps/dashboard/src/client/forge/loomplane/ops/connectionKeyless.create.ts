import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';
import { codec_KeylessInputBody } from '../codecs/KeylessInputBody';

export const op_connectionKeyless_create = {
  method: 'POST',
  path: '/api/v1/connections/keyless',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_KeylessInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
