import type { OperationMeta } from '../ops';
import { codec_DeployResult } from '../codecs/DeployResult';

export const op_aiModelDeployments_moveToPin = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}/move-to-pin',
  provides: [],
  invalidates: [],
  responseCodec: codec_DeployResult,
} as const satisfies OperationMeta;
