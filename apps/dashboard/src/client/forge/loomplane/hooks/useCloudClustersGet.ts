import { query } from '@forge-go/client-core';
import { op_cloudClusters_get } from '../ops/cloudClusters.get';
import type { CloudCluster } from '../types';

export const useCloudClustersGet = /*#__PURE__*/ query<CloudCluster>(op_cloudClusters_get);
