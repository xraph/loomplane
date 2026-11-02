import type { OperationMeta } from '../ops';
import { codec_ProviderResourceSync } from '../codecs/ProviderResourceSync';

export const op_providerResources_sync = {
  method: 'POST',
  path: '/api/v1/connections/{connectionId}/resource-syncs',
  entity: 'ProviderResourceSync',
  rootType: 'ProviderResourceSync',
  provides: ['ProviderResourceSync:{id}'],
  invalidates: ['ProviderResourceSync[]'],
  responseCodec: codec_ProviderResourceSync,
} as const satisfies OperationMeta;
