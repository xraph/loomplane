import type { OperationMeta } from '../ops';
import { codec_PushConfig } from '../codecs/PushConfig';

export const op_push_config_get = {
  method: 'GET',
  path: '/api/v1/push/config',
  provides: [],
  invalidates: [],
  responseCodec: codec_PushConfig,
} as const satisfies OperationMeta;
