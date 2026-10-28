import type { OperationMeta } from '../ops';
import { codec_CapacityWaitInputBody } from '../codecs/CapacityWaitInputBody';
import { codec_Deployment } from '../codecs/Deployment';

export const op_deployments_cancelCapacity = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/capacity/cancel',
  entity: 'Deployment',
  rootType: 'Deployment',
  provides: ['Deployment:{id}'],
  invalidates: ['Deployment[]'],
  bodyCodec: codec_CapacityWaitInputBody,
  responseCodec: codec_Deployment,
} as const satisfies OperationMeta;
