import type { OperationMeta } from '../ops';
import { codec_ProviderKey } from '../codecs/ProviderKey';
import { codec_ProviderKeyInputBody } from '../codecs/ProviderKeyInputBody';

export const op_aiSettings_putProviderKey = {
  method: 'PUT',
  path: '/api/v1/ai/settings/provider-keys/{name}',
  entity: 'ProviderKey',
  rootType: 'ProviderKey',
  provides: ['ProviderKey:{id}'],
  invalidates: ['ProviderKey[]'],
  bodyCodec: codec_ProviderKeyInputBody,
  responseCodec: codec_ProviderKey,
} as const satisfies OperationMeta;
