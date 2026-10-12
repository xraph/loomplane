import { query } from '@forge-go/client-core';
import { op_connections_get } from '../ops/connections.get';
import type { Connection } from '../types';

export const useConnectionsGet = /*#__PURE__*/ query<Connection>(op_connections_get);
