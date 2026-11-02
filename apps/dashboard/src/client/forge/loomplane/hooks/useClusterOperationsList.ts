import { query } from '@forge-go/client-core';
import { op_clusterOperations_list } from '../ops/clusterOperations.list';
import type { CloudClusterOperationPage } from '../types';

export const useClusterOperationsList = /*#__PURE__*/ query<CloudClusterOperationPage>(op_clusterOperations_list);
