import type { OperationMeta } from '../ops';
import { codec_Snapshot } from '../codecs/Snapshot';

export const op_realtime_snapshot = {
  method: 'GET',
  path: '/api/v1/realtime/snapshot',
  provides: [],
  invalidates: [],
  responseCodec: codec_Snapshot,
} as const satisfies OperationMeta;
