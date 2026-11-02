import type { OperationMeta } from '../ops';
import { codec_SpendReport } from '../codecs/SpendReport';

export const op_usage_spend = {
  method: 'GET',
  path: '/api/v1/usage/spend',
  provides: [],
  invalidates: [],
  responseCodec: codec_SpendReport,
} as const satisfies OperationMeta;
