import type { OperationMeta } from '../ops';
import { codec_Deployment } from '../codecs/Deployment';

export const op_deployments_get = {
  method: 'GET',
  path: '/api/v1/deployments/{id}',
  entity: 'Deployment',
  rootType: 'Deployment',
  provides: ['Deployment:{id}'],
  invalidates: [],
  responseCodec: codec_Deployment,
} as const satisfies OperationMeta;
