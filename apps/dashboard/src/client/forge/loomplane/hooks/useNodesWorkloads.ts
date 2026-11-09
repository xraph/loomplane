import { query } from '@forge-go/client-core';
import { op_nodes_workloads } from '../ops/nodes.workloads';
import type { NodeWorkloadList } from '../types';

export const useNodesWorkloads = /*#__PURE__*/ query<NodeWorkloadList>(op_nodes_workloads);
