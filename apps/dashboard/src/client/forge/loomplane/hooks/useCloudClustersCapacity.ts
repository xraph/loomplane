import { query } from '@forge-go/client-core';
import { op_cloudClusters_capacity } from '../ops/cloudClusters.capacity';
import type { ClusterCapacity } from '../types';

export const useCloudClustersCapacity = /*#__PURE__*/ query<ClusterCapacity>(op_cloudClusters_capacity);
