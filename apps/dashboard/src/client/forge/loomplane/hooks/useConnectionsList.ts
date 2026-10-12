import { query } from '@forge-go/client-core';
import { op_connections_list } from '../ops/connections.list';
import type { ConnectionList } from '../types';

export const useConnectionsList = /*#__PURE__*/ query<ConnectionList>(op_connections_list);
