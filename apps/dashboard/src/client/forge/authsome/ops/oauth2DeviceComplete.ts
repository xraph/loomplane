import type { OperationMeta } from '../ops';
import { codec_DeviceCompleteResponse } from '../codecs/DeviceCompleteResponse';

export const op_oauth2DeviceComplete = {
  method: 'POST',
  path: '/authsome/v1/oauth/device/complete',
  provides: [],
  invalidates: [],
  security: ['session'],
  responseCodec: codec_DeviceCompleteResponse,
} as const satisfies OperationMeta;
