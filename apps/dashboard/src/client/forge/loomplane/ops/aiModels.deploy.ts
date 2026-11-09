import type { OperationMeta } from '../ops';
import { codec_DeployInputBody } from '../codecs/DeployInputBody';
import { codec_DeployResult } from '../codecs/DeployResult';

export const op_aiModels_deploy = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models',
  provides: [],
  invalidates: [],
  bodyCodec: codec_DeployInputBody,
  responseCodec: codec_DeployResult,
} as const satisfies OperationMeta;
