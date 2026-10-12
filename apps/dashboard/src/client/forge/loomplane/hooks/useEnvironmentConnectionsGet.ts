import { query } from '@forge-go/client-core';
import { op_environmentConnections_get } from '../ops/environmentConnections.get';
import type { EnvironmentConnections } from '../types';

export const useEnvironmentConnectionsGet = /*#__PURE__*/ query<EnvironmentConnections>(op_environmentConnections_get);
