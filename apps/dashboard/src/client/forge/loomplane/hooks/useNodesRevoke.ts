import { mutation } from '@forge-go/client-core';
import { op_nodes_revoke } from '../ops/nodes.revoke';
import type { Node } from '../types';

export const useNodesRevoke = /*#__PURE__*/ mutation<Node, Node>(op_nodes_revoke);
