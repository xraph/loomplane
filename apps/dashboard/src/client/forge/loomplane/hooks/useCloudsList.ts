import { query } from '@forge-go/client-core';
import { op_clouds_list } from '../ops/clouds.list';
import type { PlanningCloudList } from '../types';

export const useCloudsList = /*#__PURE__*/ query<PlanningCloudList>(op_clouds_list);
