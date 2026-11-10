import type { OperationMeta } from '../ops';
import { codec_Flag } from '../codecs/Flag';

export const op_platformAgentFlags_dismiss = {
  method: 'POST',
  path: '/api/v1/platform/agent/flags/{id}/dismiss',
  entity: 'Flag',
  rootType: 'Flag',
  provides: ['Flag:{id}'],
  invalidates: ['Flag[]'],
  responseCodec: codec_Flag,
} as const satisfies OperationMeta;
