import type { OperationMeta } from '../ops';
import { codec_Deployment } from '../codecs/Deployment';

export const op_deployments_forPlan = {
  method: 'GET',
  path: '/api/v1/plans/{id}/deployment',
  entity: 'Deployment',
  rootType: 'Deployment',
  provides: ['Deployment:{id}'],
  invalidates: [],
  responseCodec: codec_Deployment,
} as const satisfies OperationMeta;
