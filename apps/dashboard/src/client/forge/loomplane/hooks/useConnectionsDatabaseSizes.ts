import { query } from '@forge-go/client-core';
import { op_connections_databaseSizes } from '../ops/connections.databaseSizes';
import type { DatabaseSizes } from '../types';

export const useConnectionsDatabaseSizes = /*#__PURE__*/ query<DatabaseSizes>(op_connections_databaseSizes);
