import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';

export const op_catalogEndpoints_clearAdmin = {
  method: 'DELETE',
  path: '/api/v1/catalog/endpoints/{id}/admin',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: [],
  invalidates: ['CatalogconnectEndpoint[]'],
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
