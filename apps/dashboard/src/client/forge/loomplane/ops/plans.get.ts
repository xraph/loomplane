import type { OperationMeta } from '../ops';
import { codec_Plan } from '../codecs/Plan';

export const op_plans_get = {
  method: 'GET',
  path: '/api/v1/plans/{id}',
  entity: 'Plan',
  rootType: 'Plan',
  provides: ['Plan:{id}'],
  invalidates: [],
  responseCodec: codec_Plan,
} as const satisfies OperationMeta;
