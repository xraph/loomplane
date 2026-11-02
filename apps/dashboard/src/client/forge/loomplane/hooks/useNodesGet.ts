import { query } from '@forge-go/client-core';
import { op_nodes_get } from '../ops/nodes.get';
import type { Node } from '../types';

export const useNodesGet = /*#__PURE__*/ query<Node>(op_nodes_get);
