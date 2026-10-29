import type { OperationMeta } from '../ops';
import { codec_DeploymentsDeployment } from '../codecs/DeploymentsDeployment';

export const op_deployments_get = {
  method: 'GET',
  path: '/api/v1/deployments/{id}',
  entity: 'DeploymentsDeployment',
  rootType: 'DeploymentsDeployment',
  provides: ['DeploymentsDeployment:{id}'],
  invalidates: [],
  responseCodec: codec_DeploymentsDeployment,
} as const satisfies OperationMeta;
