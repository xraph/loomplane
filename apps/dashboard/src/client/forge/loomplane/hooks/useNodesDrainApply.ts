import { mutation } from '@forge-go/client-core';
import { op_nodes_drainApply } from '../ops/nodes.drainApply';
import type { Node } from '../types';

export const useNodesDrainApply = /*#__PURE__*/ mutation<Node, Node>(op_nodes_drainApply);
