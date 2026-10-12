import type { OperationMeta } from '../ops';
import { codec_CreatedKey } from '../codecs/CreatedKey';
import { codec_NewKeyBody } from '../codecs/NewKeyBody';

export const op_apiKeys_create = {
  method: 'POST',
  path: '/api/v1/api-keys',
  rootType: 'CreatedKey',
  provides: [],
  invalidates: [],
  bodyCodec: codec_NewKeyBody,
  responseCodec: codec_CreatedKey,
} as const satisfies OperationMeta;
