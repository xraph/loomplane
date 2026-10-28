import type { OperationMeta } from '../ops';
import { codec_ModelsStatus } from '../codecs/ModelsStatus';

export const op_aiModelDeployments_get = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}',
  rootType: 'ModelsStatus',
  provides: [],
  invalidates: [],
  responseCodec: codec_ModelsStatus,
} as const satisfies OperationMeta;
