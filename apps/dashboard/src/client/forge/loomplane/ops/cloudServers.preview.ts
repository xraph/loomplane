import type { OperationMeta } from '../ops';
import { codec_Operation } from '../codecs/Operation';
import { codec_ServerInputBody } from '../codecs/ServerInputBody';

export const op_cloudServers_preview = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/servers/preview',
  entity: 'Operation',
  rootType: 'Operation',
  provides: ['Operation:{id}'],
  invalidates: ['Operation[]'],
  bodyCodec: codec_ServerInputBody,
  responseCodec: codec_Operation,
} as const satisfies OperationMeta;
