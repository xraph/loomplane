import type { OperationMeta } from '../ops';
import { codec_CloseInputBody } from '../codecs/CloseInputBody';
import { codec_Report } from '../codecs/Report';

export const op_agentReports_close = {
  method: 'POST',
  path: '/api/v1/agent/reports/{id}/close',
  entity: 'Report',
  rootType: 'Report',
  provides: ['Report:{id}'],
  invalidates: ['Report[]'],
  bodyCodec: codec_CloseInputBody,
  responseCodec: codec_Report,
} as const satisfies OperationMeta;
