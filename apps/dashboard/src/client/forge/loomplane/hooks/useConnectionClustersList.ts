import { query } from '@forge-go/client-core';
import { op_connectionClusters_list } from '../ops/connectionClusters.list';
import type { ClusterPage } from '../types';

export const useConnectionClustersList = /*#__PURE__*/ query<ClusterPage>(op_connectionClusters_list);
