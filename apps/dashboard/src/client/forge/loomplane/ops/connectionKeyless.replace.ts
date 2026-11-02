import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';
import { codec_KeylessReplaceInputBody } from '../codecs/KeylessReplaceInputBody';

export const op_connectionKeyless_replace = {
  method: 'PUT',
  path: '/api/v1/connections/{id}/keyless',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_KeylessReplaceInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
