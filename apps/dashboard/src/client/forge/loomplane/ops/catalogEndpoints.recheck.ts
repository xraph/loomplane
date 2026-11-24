import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';
import { codec_RecheckInputBody } from '../codecs/RecheckInputBody';

export const op_catalogEndpoints_recheck = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints/{id}/checks',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_RecheckInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
