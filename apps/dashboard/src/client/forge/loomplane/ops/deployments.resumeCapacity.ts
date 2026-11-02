import type { OperationMeta } from '../ops';
import { codec_CapacityWaitInputBody } from '../codecs/CapacityWaitInputBody';
import { codec_DeploymentsDeployment } from '../codecs/DeploymentsDeployment';

export const op_deployments_resumeCapacity = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/capacity/resume',
  entity: 'DeploymentsDeployment',
  rootType: 'DeploymentsDeployment',
  provides: ['DeploymentsDeployment:{id}'],
  invalidates: ['DeploymentsDeployment[]'],
  bodyCodec: codec_CapacityWaitInputBody,
  responseCodec: codec_DeploymentsDeployment,
} as const satisfies OperationMeta;
