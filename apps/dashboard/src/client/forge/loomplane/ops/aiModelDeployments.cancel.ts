import type { OperationMeta } from '../ops';
import { codec_CancelResult } from '../codecs/CancelResult';

export const op_aiModelDeployments_cancel = {
  method: 'DELETE',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}/waiting',
  provides: [],
  invalidates: [],
  responseCodec: codec_CancelResult,
} as const satisfies OperationMeta;
