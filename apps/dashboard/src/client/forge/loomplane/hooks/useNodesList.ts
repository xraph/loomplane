import { query } from '@forge-go/client-core';
import { op_nodes_list } from '../ops/nodes.list';
import type { NodeList } from '../types';

export const useNodesList = /*#__PURE__*/ query<NodeList>(op_nodes_list);
