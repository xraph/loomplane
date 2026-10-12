import type { OperationMeta } from '../ops';
import { codec_PublicConfig } from '../codecs/PublicConfig';

export const op_publicConfig_get = {
  method: 'GET',
  path: '/api/v1/public-config',
  provides: [],
  invalidates: [],
  responseCodec: codec_PublicConfig,
} as const satisfies OperationMeta;
