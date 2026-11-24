import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';

export const op_catalogEndpoints_get = {
  method: 'GET',
  path: '/api/v1/catalog/endpoints/{id}',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: [],
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
