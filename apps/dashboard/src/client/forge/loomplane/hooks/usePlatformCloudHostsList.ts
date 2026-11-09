import { query } from '@forge-go/client-core';
import { op_platformCloudHosts_list } from '../ops/platformCloudHosts.list';
import type { HostList } from '../types';

export const usePlatformCloudHostsList = /*#__PURE__*/ query<HostList>(op_platformCloudHosts_list);
