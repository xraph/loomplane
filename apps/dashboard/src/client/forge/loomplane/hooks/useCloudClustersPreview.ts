import { mutation } from '@forge-go/client-core';
import { op_cloudClusters_preview } from '../ops/cloudClusters.preview';
import type { CloudClusterOperation } from '../types';

export const useCloudClustersPreview = /*#__PURE__*/ mutation<CloudClusterOperation, CloudClusterOperation>(op_cloudClusters_preview);
