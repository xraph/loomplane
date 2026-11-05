import { query } from '@forge-go/client-core';
import { op_platformCloudRegions_list } from '../ops/platformCloudRegions.list';
import type { RegionList } from '../types';

export const usePlatformCloudRegionsList = /*#__PURE__*/ query<RegionList>(op_platformCloudRegions_list);
