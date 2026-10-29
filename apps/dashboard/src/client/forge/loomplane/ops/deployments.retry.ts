import type { OperationMeta } from '../ops';
import { codec_DeploymentsDeployment } from '../codecs/DeploymentsDeployment';

export const op_deployments_retry = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/retry',
  entity: 'DeploymentsDeployment',
  rootType: 'DeploymentsDeployment',
  provides: ['DeploymentsDeployment:{id}'],
  invalidates: ['DeploymentsDeployment[]'],
  responseCodec: codec_DeploymentsDeployment,
} as const satisfies OperationMeta;
