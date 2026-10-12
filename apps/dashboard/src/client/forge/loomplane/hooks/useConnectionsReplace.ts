import { mutation } from '@forge-go/client-core';
import { op_connections_replace } from '../ops/connections.replace';
import type { Connection } from '../types';

export const useConnectionsReplace = /*#__PURE__*/ mutation<Connection, Connection>(op_connections_replace);
