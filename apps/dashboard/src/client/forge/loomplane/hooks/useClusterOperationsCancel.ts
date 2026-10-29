import { mutation } from '@forge-go/client-core';
import { op_clusterOperations_cancel } from '../ops/clusterOperations.cancel';
import type { CloudClusterOperation } from '../types';

export const useClusterOperationsCancel = /*#__PURE__*/ mutation<CloudClusterOperation, CloudClusterOperation>(op_clusterOperations_cancel);
