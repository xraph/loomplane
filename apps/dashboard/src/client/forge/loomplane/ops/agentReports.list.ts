import type { OperationMeta } from '../ops';
import { codec_ReportList } from '../codecs/ReportList';

export const op_agentReports_list = {
  method: 'GET',
  path: '/api/v1/agent/reports',
  entity: 'Report',
  rootType: 'ReportList',
  provides: ['Report:{id}', 'Report[]'],
  invalidates: [],
  responseCodec: codec_ReportList,
} as const satisfies OperationMeta;
