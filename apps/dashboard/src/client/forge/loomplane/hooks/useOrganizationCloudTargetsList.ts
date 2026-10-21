import { query } from '@forge-go/client-core';
import { op_organizationCloudTargets_list } from '../ops/organizationCloudTargets.list';
import type { CloudTargetList } from '../types';

export const useOrganizationCloudTargetsList = /*#__PURE__*/ query<CloudTargetList>(op_organizationCloudTargets_list);
