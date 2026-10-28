import type { OperationMeta } from '../ops';
import { codec_DeployInputBody } from '../codecs/DeployInputBody';
import { codec_DeployPreview } from '../codecs/DeployPreview';

export const op_aiModels_preview = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/preview',
  rootType: 'DeployPreview',
  provides: [],
  invalidates: [],
  bodyCodec: codec_DeployInputBody,
  responseCodec: codec_DeployPreview,
} as const satisfies OperationMeta;
