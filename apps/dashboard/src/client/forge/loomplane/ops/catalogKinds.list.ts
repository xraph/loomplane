import type { OperationMeta } from '../ops';
import { codec_CatalogKindList } from '../codecs/CatalogKindList';

export const op_catalogKinds_list = {
  method: 'GET',
  path: '/api/v1/catalog/kinds',
  provides: [],
  invalidates: [],
  responseCodec: codec_CatalogKindList,
} as const satisfies OperationMeta;
