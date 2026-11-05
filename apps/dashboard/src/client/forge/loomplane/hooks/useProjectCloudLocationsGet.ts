import { query } from '@forge-go/client-core';
import { op_projectCloudLocations_get } from '../ops/projectCloudLocations.get';
import type { CloudLocation } from '../types';

export const useProjectCloudLocationsGet = /*#__PURE__*/ query<CloudLocation>(op_projectCloudLocations_get);
