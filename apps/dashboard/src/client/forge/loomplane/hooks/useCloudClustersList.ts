import { query } from '@forge-go/client-core';
import { op_cloudClusters_list } from '../ops/cloudClusters.list';
import type { CloudClusterPage } from '../types';

export const useCloudClustersList = /*#__PURE__*/ query<CloudClusterPage>(op_cloudClusters_list);
