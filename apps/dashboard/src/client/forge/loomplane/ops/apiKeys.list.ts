import type { OperationMeta } from '../ops';
import { codec_APIKeyList } from '../codecs/APIKeyList';

export const op_apiKeys_list = {
  method: 'GET',
  path: '/api/v1/api-keys',
  entity: 'APIKey',
  rootType: 'APIKeyList',
  provides: ['APIKey:{id}', 'APIKey[]'],
  invalidates: [],
  responseCodec: codec_APIKeyList,
} as const satisfies OperationMeta;
