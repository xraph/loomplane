import { mutation } from '@forge-go/client-core';
import { op_nodes_drain } from '../ops/nodes.drain';

export const useNodesDrain = /*#__PURE__*/ mutation(op_nodes_drain);
