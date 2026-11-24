import type { OperationMeta } from '../ops';
import { codec_AdminCredentialInputBody } from '../codecs/AdminCredentialInputBody';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';

export const op_catalogEndpoints_setAdmin = {
  method: 'PUT',
  path: '/api/v1/catalog/endpoints/{id}/admin',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_AdminCredentialInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
