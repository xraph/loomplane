import type { OperationMeta } from '../ops';
import { codec_TwoStep } from '../codecs/TwoStep';

export const op_account_twoStep_get = {
  method: 'GET',
  path: '/api/v1/account/two-step',
  provides: [],
  invalidates: [],
  responseCodec: codec_TwoStep,
} as const satisfies OperationMeta;
