import type { OperationMeta } from '../ops';
import { codec_PlanningCloudList } from '../codecs/PlanningCloudList';

export const op_organizationClouds_list = {
  method: 'GET',
  path: '/api/v1/clouds',
  provides: [],
  invalidates: [],
  responseCodec: codec_PlanningCloudList,
} as const satisfies OperationMeta;
