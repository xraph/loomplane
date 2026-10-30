import type { OperationMeta } from '../ops';
import { codec_SetupStatus } from '../codecs/SetupStatus';

export const op_setup_status = {
  method: 'GET',
  path: '/api/v1/setup',
  provides: [],
  invalidates: [],
  responseCodec: codec_SetupStatus,
} as const satisfies OperationMeta;
