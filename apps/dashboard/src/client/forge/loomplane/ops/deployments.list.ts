import type { OperationMeta } from '../ops';
import { codec_DeploymentList } from '../codecs/DeploymentList';

export const op_deployments_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/deployments',
  entity: 'DeploymentsDeployment',
  rootType: 'DeploymentList',
  provides: ['DeploymentsDeployment:{id}', 'DeploymentsDeployment[]'],
  invalidates: [],
  responseCodec: codec_DeploymentList,
} as const satisfies OperationMeta;
