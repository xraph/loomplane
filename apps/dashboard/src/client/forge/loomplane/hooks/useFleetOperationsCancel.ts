import { mutation } from '@forge-go/client-core';
import { op_fleetOperations_cancel } from '../ops/fleetOperations.cancel';
import type { FleetOperation } from '../types';

export const useFleetOperationsCancel = /*#__PURE__*/ mutation<FleetOperation, FleetOperation>(op_fleetOperations_cancel);
