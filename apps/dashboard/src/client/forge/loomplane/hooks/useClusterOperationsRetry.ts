import { mutation } from '@forge-go/client-core';
import { op_clusterOperations_retry } from '../ops/clusterOperations.retry';
import type { CloudClusterOperation } from '../types';

export const useClusterOperationsRetry = /*#__PURE__*/ mutation<CloudClusterOperation, CloudClusterOperation>(op_clusterOperations_retry);
