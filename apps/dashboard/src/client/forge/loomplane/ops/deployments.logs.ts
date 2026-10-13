import type { OperationMeta } from '../ops';
import { codec_LogPage } from '../codecs/LogPage';

export const op_deployments_logs = {
  method: 'GET',
  path: '/api/v1/deployments/{id}/logs',
  entity: 'Log',
  rootType: 'LogPage',
  provides: ['Log:{id}', 'Log[]'],
  invalidates: [],
  responseCodec: codec_LogPage,
} as const satisfies OperationMeta;
