import type { OperationMeta } from '../ops';
import { codec_TrafficReport } from '../codecs/TrafficReport';

export const op_usage_traffic = {
  method: 'GET',
  path: '/api/v1/usage/traffic',
  provides: [],
  invalidates: [],
  responseCodec: codec_TrafficReport,
} as const satisfies OperationMeta;
