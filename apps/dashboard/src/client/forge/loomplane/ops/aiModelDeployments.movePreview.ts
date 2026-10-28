import type { OperationMeta } from '../ops';
import { codec_DeployPreview } from '../codecs/DeployPreview';
import { codec_ModelsMoveInputBody } from '../codecs/ModelsMoveInputBody';

export const op_aiModelDeployments_movePreview = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}/move-preview',
  rootType: 'DeployPreview',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ModelsMoveInputBody,
  responseCodec: codec_DeployPreview,
} as const satisfies OperationMeta;
