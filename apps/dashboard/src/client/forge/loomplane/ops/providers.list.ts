import type { OperationMeta } from '../ops';
import { codec_ProviderList } from '../codecs/ProviderList';

export const op_providers_list = {
  method: 'GET',
  path: '/api/v1/providers',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProviderList,
} as const satisfies OperationMeta;
