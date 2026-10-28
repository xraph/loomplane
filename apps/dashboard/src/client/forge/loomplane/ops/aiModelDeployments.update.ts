import type { OperationMeta } from '../ops';
import { codec_ModelsStatus } from '../codecs/ModelsStatus';
import { codec_ModelsUpdateInputBody } from '../codecs/ModelsUpdateInputBody';

export const op_aiModelDeployments_update = {
  method: 'PATCH',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}',
  rootType: 'ModelsStatus',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ModelsUpdateInputBody,
  responseCodec: codec_ModelsStatus,
} as const satisfies OperationMeta;
