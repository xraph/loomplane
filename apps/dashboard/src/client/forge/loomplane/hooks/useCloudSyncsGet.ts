import { query } from '@forge-go/client-core';
import { op_cloudSyncs_get } from '../ops/cloudSyncs.get';
import type { Sync } from '../types';

export const useCloudSyncsGet = /*#__PURE__*/ query<Sync>(op_cloudSyncs_get);
