import { query } from '@forge-go/client-core';
import { op_cloudLocations_list } from '../ops/cloudLocations.list';
import type { CloudLocationList } from '../types';

export const useCloudLocationsList = /*#__PURE__*/ query<CloudLocationList>(op_cloudLocations_list);
