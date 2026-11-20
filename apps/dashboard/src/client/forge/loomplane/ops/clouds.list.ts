import type { OperationMeta } from '../ops';
import { codec_PlanningCloudList } from '../codecs/PlanningCloudList';

export const op_clouds_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/clouds',
  entity: 'Profile',
  rootType: 'PlanningCloudList',
  provides: ['Profile:{id}', 'Profile[]'],
  invalidates: [],
  responseCodec: codec_PlanningCloudList,
} as const satisfies OperationMeta;
