import { mutation } from '@forge-go/client-core';
import { op_connectionInCluster_create } from '../ops/connectionInCluster.create';
import type { Connection } from '../types';

export const useConnectionInClusterCreate = /*#__PURE__*/ mutation<Connection, Connection>(op_connectionInCluster_create);
