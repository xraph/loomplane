import { query } from '@forge-go/client-core';
import { op_organizationCloudSyncs_list } from '../ops/organizationCloudSyncs.list';
import type { CloudSyncList } from '../types';

export const useOrganizationCloudSyncsList = /*#__PURE__*/ query<CloudSyncList>(op_organizationCloudSyncs_list);
