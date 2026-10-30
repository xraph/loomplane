import { query } from '@forge-go/client-core';
import { op_clusterOperations_get } from '../ops/clusterOperations.get';
import type { CloudClusterOperation } from '../types';

export const useClusterOperationsGet = /*#__PURE__*/ query<CloudClusterOperation>(op_clusterOperations_get);
