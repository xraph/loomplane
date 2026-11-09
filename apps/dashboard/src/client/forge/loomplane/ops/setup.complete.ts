import type { OperationMeta } from '../ops';
import { codec_SetupRequestBody } from '../codecs/SetupRequestBody';
import { codec_SetupResponse } from '../codecs/SetupResponse';

export const op_setup_complete = {
  method: 'POST',
  path: '/api/v1/setup',
  provides: [],
  invalidates: [],
  bodyCodec: codec_SetupRequestBody,
  responseCodec: codec_SetupResponse,
} as const satisfies OperationMeta;
