import { query } from '@forge-go/client-core';
import { op_cloudClusters_options } from '../ops/cloudClusters.options';
import type { ClusterOptions } from '../types';

export const useCloudClustersOptions = /*#__PURE__*/ query<ClusterOptions>(op_cloudClusters_options);
