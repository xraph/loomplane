import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';
import { codec_SharingInputBody } from '../codecs/SharingInputBody';

export const op_catalogEndpoints_share = {
  method: 'PUT',
  path: '/api/v1/catalog/endpoints/{id}/sharing',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_SharingInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
