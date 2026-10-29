import type { OperationMeta } from '../ops';
import { codec_TestPushResult } from '../codecs/TestPushResult';

export const op_push_devices_test = {
  method: 'POST',
  path: '/api/v1/push/devices/{id}/test',
  provides: [],
  invalidates: [],
  responseCodec: codec_TestPushResult,
} as const satisfies OperationMeta;
