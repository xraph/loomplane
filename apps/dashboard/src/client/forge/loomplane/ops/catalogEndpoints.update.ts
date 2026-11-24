import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';
import { codec_DetailsBody } from '../codecs/DetailsBody';

export const op_catalogEndpoints_update = {
  method: 'PATCH',
  path: '/api/v1/catalog/endpoints/{id}',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_DetailsBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
