import type { OperationMeta } from '../ops';
import { codec_ReportList } from '../codecs/ReportList';

export const op_agentReports_list = {
  method: 'GET',
  path: '/api/v1/agent/reports',
  provides: [],
  invalidates: [],
  responseCodec: codec_ReportList,
} as const satisfies OperationMeta;
