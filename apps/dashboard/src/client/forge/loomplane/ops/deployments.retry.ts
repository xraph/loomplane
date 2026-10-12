import type { OperationMeta } from '../ops';
import { codec_Deployment } from '../codecs/Deployment';

export const op_deployments_retry = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/retry',
  entity: 'Deployment',
  rootType: 'Deployment',
  provides: ['Deployment:{id}'],
  invalidates: ['Deployment[]'],
  responseCodec: codec_Deployment,
} as const satisfies OperationMeta;
