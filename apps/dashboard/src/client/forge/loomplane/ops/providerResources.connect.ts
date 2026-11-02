import type { OperationMeta } from '../ops';
import { codec_ProviderResource } from '../codecs/ProviderResource';
import { codec_ProviderResourceConnectInputBody } from '../codecs/ProviderResourceConnectInputBody';

export const op_providerResources_connect = {
  method: 'PUT',
  path: '/api/v1/provider-resources/{resourceId}/connection',
  entity: 'ProviderResource',
  rootType: 'ProviderResource',
  provides: ['ProviderResource:{id}'],
  invalidates: ['ProviderResource[]'],
  bodyCodec: codec_ProviderResourceConnectInputBody,
  responseCodec: codec_ProviderResource,
} as const satisfies OperationMeta;
