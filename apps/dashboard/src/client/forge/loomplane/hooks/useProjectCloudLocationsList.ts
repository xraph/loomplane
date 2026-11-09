import { query } from '@forge-go/client-core';
import { op_projectCloudLocations_list } from '../ops/projectCloudLocations.list';
import type { CloudLocationList } from '../types';

export const useProjectCloudLocationsList = /*#__PURE__*/ query<CloudLocationList>(op_projectCloudLocations_list);
