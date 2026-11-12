import { mutation } from '@forge-go/client-core';
import { op_fleetInstances_requestAction } from '../ops/fleetInstances.requestAction';
import type { FleetOperation } from '../types';

export const useFleetInstancesRequestAction = /*#__PURE__*/ mutation<FleetOperation, FleetOperation>(op_fleetInstances_requestAction);
