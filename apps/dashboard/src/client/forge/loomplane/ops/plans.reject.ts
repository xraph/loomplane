import type { OperationMeta } from '../ops';
import { codec_DecideInputBody } from '../codecs/DecideInputBody';
import { codec_Plan } from '../codecs/Plan';

export const op_plans_reject = {
  method: 'POST',
  path: '/api/v1/plans/{id}/reject',
  entity: 'Plan',
  rootType: 'Plan',
  provides: ['Plan:{id}'],
  invalidates: ['Plan[]'],
  bodyCodec: codec_DecideInputBody,
  responseCodec: codec_Plan,
} as const satisfies OperationMeta;
