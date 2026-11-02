import { mutation } from '@forge-go/client-core';
import { op_cloudClusters_refreshCapacity } from '../ops/cloudClusters.refreshCapacity';
import type { ClusterCapacity } from '../types';

export const useCloudClustersRefreshCapacity = /*#__PURE__*/ mutation<ClusterCapacity, ClusterCapacity>(op_cloudClusters_refreshCapacity);
