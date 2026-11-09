import { query } from '@forge-go/client-core';
import { op_platformCloudTenants_list } from '../ops/platformCloudTenants.list';
import type { PlatformcloudTenantList } from '../types';

export const usePlatformCloudTenantsList = /*#__PURE__*/ query<PlatformcloudTenantList>(op_platformCloudTenants_list);
