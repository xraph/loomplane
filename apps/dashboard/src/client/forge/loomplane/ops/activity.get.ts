import type { OperationMeta } from '../ops';
import { codec_ActivitySummary } from '../codecs/ActivitySummary';

export const op_activity_get = {
  method: 'GET',
  path: '/api/v1/activity',
  provides: [],
  invalidates: [],
  responseCodec: codec_ActivitySummary,
} as const satisfies OperationMeta;
