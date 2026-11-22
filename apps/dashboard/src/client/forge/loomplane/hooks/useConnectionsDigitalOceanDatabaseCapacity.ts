import { query } from '@forge-go/client-core';
import { op_connections_digitalOceanDatabaseCapacity } from '../ops/connections.digitalOceanDatabaseCapacity';
import type { DatabaseCapacity } from '../types';

export const useConnectionsDigitalOceanDatabaseCapacity = /*#__PURE__*/ query<DatabaseCapacity>(op_connections_digitalOceanDatabaseCapacity);
