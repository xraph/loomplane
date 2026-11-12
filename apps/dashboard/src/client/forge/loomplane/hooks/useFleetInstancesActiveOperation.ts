import { query } from '@forge-go/client-core';
import { op_fleetInstances_activeOperation } from '../ops/fleetInstances.activeOperation';
import type { FleetOperation } from '../types';

export const useFleetInstancesActiveOperation = /*#__PURE__*/ query<FleetOperation>(op_fleetInstances_activeOperation);
