import { query } from '@forge-go/client-core';
import { op_organizationClouds_list } from '../ops/organizationClouds.list';
import type { PlanningCloudList } from '../types';

export const useOrganizationCloudsList = /*#__PURE__*/ query<PlanningCloudList>(op_organizationClouds_list);
