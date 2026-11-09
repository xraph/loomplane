import type { OperationMeta } from '../ops';
import { codec_ProviderEnvironmentList } from '../codecs/ProviderEnvironmentList';

export const op_providerEnvironments_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/provider-environments',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProviderEnvironmentList,
} as const satisfies OperationMeta;
