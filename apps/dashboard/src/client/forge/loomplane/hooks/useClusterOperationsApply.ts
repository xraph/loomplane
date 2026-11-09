import { mutation } from '@forge-go/client-core';
import { op_clusterOperations_apply } from '../ops/clusterOperations.apply';
import type { CloudClusterOperation } from '../types';

export const useClusterOperationsApply = /*#__PURE__*/ mutation<CloudClusterOperation, CloudClusterOperation>(op_clusterOperations_apply);
