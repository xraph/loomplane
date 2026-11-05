import { mutation } from '@forge-go/client-core';
import { op_platformCloudRegions_update } from '../ops/platformCloudRegions.update';
import type { Region } from '../types';

export const usePlatformCloudRegionsUpdate = /*#__PURE__*/ mutation<Region, Region>(op_platformCloudRegions_update);
