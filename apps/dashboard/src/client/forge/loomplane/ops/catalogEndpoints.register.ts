import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';
import { codec_RegisterServiceInputBody } from '../codecs/RegisterServiceInputBody';

export const op_catalogEndpoints_register = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints/registrations',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_RegisterServiceInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
