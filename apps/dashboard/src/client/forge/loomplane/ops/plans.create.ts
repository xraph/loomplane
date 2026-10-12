import type { OperationMeta } from '../ops';
import { codec_CreatePlanInputBody } from '../codecs/CreatePlanInputBody';
import { codec_Plan } from '../codecs/Plan';

export const op_plans_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/plans',
  entity: 'Plan',
  rootType: 'Plan',
  provides: ['Plan:{id}'],
  invalidates: ['Plan[]'],
  bodyCodec: codec_CreatePlanInputBody,
  responseCodec: codec_Plan,
} as const satisfies OperationMeta;
