import { mutation } from '@forge-go/client-core';
import { op_connections_check } from '../ops/connections.check';
import type { Connection } from '../types';

export const useConnectionsCheck = /*#__PURE__*/ mutation<Connection, Connection>(op_connections_check);
