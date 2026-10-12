import { mutation } from '@forge-go/client-core';
import { op_connections_delete } from '../ops/connections.delete';

export const useConnectionsDelete = /*#__PURE__*/ mutation(op_connections_delete);
