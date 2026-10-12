import { mutation } from '@forge-go/client-core';
import { op_connections_create } from '../ops/connections.create';
import type { Connection } from '../types';

export const useConnectionsCreate = /*#__PURE__*/ mutation<Connection, Connection>(op_connections_create);
