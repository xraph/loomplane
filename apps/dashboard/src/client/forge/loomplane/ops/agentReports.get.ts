import type { OperationMeta } from '../ops';
import { codec_Report } from '../codecs/Report';

export const op_agentReports_get = {
  method: 'GET',
  path: '/api/v1/agent/reports/{id}',
  entity: 'Report',
  rootType: 'Report',
  provides: ['Report:{id}'],
  invalidates: [],
  responseCodec: codec_Report,
} as const satisfies OperationMeta;
