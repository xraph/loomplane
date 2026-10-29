import type { OperationMeta } from '../ops';
import { codec_Soak } from '../codecs/Soak';

export const op_plans_soak = {
  method: 'GET',
  path: '/api/v1/plans/{id}/soak',
  provides: [],
  invalidates: [],
  responseCodec: codec_Soak,
} as const satisfies OperationMeta;
