import type { OperationMeta } from '../ops';
import { codec_FenceRepair } from '../codecs/FenceRepair';

export const op_connectionFenceRepair_review = {
  method: 'POST',
  path: '/api/v1/connections/{id}/fence-repair/review',
  provides: [],
  invalidates: [],
  responseCodec: codec_FenceRepair,
} as const satisfies OperationMeta;
