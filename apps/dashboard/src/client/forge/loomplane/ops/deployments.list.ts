import type { OperationMeta } from '../ops';
import { codec_DeploymentList } from '../codecs/DeploymentList';

export const op_deployments_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/deployments',
  entity: 'Deployment',
  rootType: 'DeploymentList',
  provides: ['Deployment:{id}', 'Deployment[]'],
  invalidates: [],
  responseCodec: codec_DeploymentList,
} as const satisfies OperationMeta;
