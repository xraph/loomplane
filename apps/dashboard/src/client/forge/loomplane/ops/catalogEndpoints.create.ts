import type { OperationMeta } from '../ops';
import { codec_CatalogconnectCreateInputBody } from '../codecs/CatalogconnectCreateInputBody';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';

export const op_catalogEndpoints_create = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_CatalogconnectCreateInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
