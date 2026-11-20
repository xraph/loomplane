import type { OperationMeta } from '../ops';
import { codec_View } from '../codecs/View';

export const op_aiSettings_get = {
  method: 'GET',
  path: '/api/v1/ai/settings',
  entity: 'ProviderKey',
  rootType: 'View',
  provides: ['ProviderKey:{id}', 'ProviderKey[]'],
  invalidates: [],
  responseCodec: codec_View,
} as const satisfies OperationMeta;
