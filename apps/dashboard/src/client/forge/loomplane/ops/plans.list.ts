import type { OperationMeta } from '../ops';
import { codec_PlanList } from '../codecs/PlanList';

export const op_plans_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/plans',
  entity: 'Plan',
  rootType: 'PlanList',
  provides: ['Plan:{id}', 'Plan[]'],
  invalidates: [],
  responseCodec: codec_PlanList,
} as const satisfies OperationMeta;
