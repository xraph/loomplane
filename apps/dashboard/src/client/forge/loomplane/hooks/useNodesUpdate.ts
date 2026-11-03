import { mutation } from '@forge-go/client-core';
import { op_nodes_update } from '../ops/nodes.update';
import type { Node } from '../types';

export const useNodesUpdate = /*#__PURE__*/ mutation<Node, Node>(op_nodes_update);
