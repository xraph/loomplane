import type { OperationMeta } from '../ops';
import { codec_ProviderResourceList } from '../codecs/ProviderResourceList';

export const op_providerResources_list = {
  method: 'GET',
  path: '/api/v1/provider-resources',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProviderResourceList,
} as const satisfies OperationMeta;
