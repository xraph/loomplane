import type { OperationMeta } from '../ops';
import { codec_DeploymentsDeployment } from '../codecs/DeploymentsDeployment';

export const op_deployments_forPlan = {
  method: 'GET',
  path: '/api/v1/plans/{id}/deployment',
  entity: 'DeploymentsDeployment',
  rootType: 'DeploymentsDeployment',
  provides: ['DeploymentsDeployment:{id}'],
  invalidates: [],
  responseCodec: codec_DeploymentsDeployment,
} as const satisfies OperationMeta;
