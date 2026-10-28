import type { OperationMeta } from '../ops';
import { codec_DeployResult } from '../codecs/DeployResult';
import { codec_ModelsMoveInputBody } from '../codecs/ModelsMoveInputBody';

export const op_aiModelDeployments_move = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}/move',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ModelsMoveInputBody,
  responseCodec: codec_DeployResult,
} as const satisfies OperationMeta;
