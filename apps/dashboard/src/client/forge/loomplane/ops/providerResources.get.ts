import type { OperationMeta } from '../ops';
import { codec_ProviderResource } from '../codecs/ProviderResource';

export const op_providerResources_get = {
  method: 'GET',
  path: '/api/v1/provider-resources/{resourceId}',
  entity: 'ProviderResource',
  rootType: 'ProviderResource',
  provides: ['ProviderResource:{id}'],
  invalidates: [],
  responseCodec: codec_ProviderResource,
} as const satisfies OperationMeta;
