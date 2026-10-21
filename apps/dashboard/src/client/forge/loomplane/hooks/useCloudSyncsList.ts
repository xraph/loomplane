import { query } from '@forge-go/client-core';
import { op_cloudSyncs_list } from '../ops/cloudSyncs.list';
import type { CloudSyncList } from '../types';

export const useCloudSyncsList = /*#__PURE__*/ query<CloudSyncList>(op_cloudSyncs_list);
