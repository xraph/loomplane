import { query } from '@forge-go/client-core';
import { op_cloudLocations_get } from '../ops/cloudLocations.get';
import type { CloudLocation } from '../types';

export const useCloudLocationsGet = /*#__PURE__*/ query<CloudLocation>(op_cloudLocations_get);
