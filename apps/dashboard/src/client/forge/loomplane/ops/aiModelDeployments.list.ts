import type { OperationMeta } from '../ops';
import { codec_ModelDeploymentList } from '../codecs/ModelDeploymentList';

export const op_aiModelDeployments_list = {
  method: 'GET',
  path: '/api/v1/ai/models/deployments',
  provides: [],
  invalidates: [],
  responseCodec: codec_ModelDeploymentList,
} as const satisfies OperationMeta;
