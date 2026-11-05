import { mutation } from '@forge-go/client-core';
import { op_platformCloudRegions_create } from '../ops/platformCloudRegions.create';
import type { Region } from '../types';

export const usePlatformCloudRegionsCreate = /*#__PURE__*/ mutation<Region, Region>(op_platformCloudRegions_create);
