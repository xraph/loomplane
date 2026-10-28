import type { OperationMeta } from '../ops';
import { codec_ModelsStatus } from '../codecs/ModelsStatus';

export const op_aiModelDeployments_retry = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/ai/models/{resource}/retry',
  rootType: 'ModelsStatus',
  provides: [],
  invalidates: [],
  responseCodec: codec_ModelsStatus,
} as const satisfies OperationMeta;
