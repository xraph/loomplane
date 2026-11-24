import type { OperationMeta } from '../ops';
import { codec_CatalogconnectEndpoint } from '../codecs/CatalogconnectEndpoint';
import { codec_ReplaceCredentialInputBody } from '../codecs/ReplaceCredentialInputBody';

export const op_catalogEndpoints_replaceCredential = {
  method: 'PUT',
  path: '/api/v1/catalog/endpoints/{id}/credential',
  entity: 'CatalogconnectEndpoint',
  rootType: 'CatalogconnectEndpoint',
  provides: ['CatalogconnectEndpoint:{id}'],
  invalidates: ['CatalogconnectEndpoint[]'],
  bodyCodec: codec_ReplaceCredentialInputBody,
  responseCodec: codec_CatalogconnectEndpoint,
} as const satisfies OperationMeta;
