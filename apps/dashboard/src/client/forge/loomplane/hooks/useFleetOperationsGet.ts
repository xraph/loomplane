import { query } from '@forge-go/client-core';
import { op_fleetOperations_get } from '../ops/fleetOperations.get';
import type { FleetOperation } from '../types';

export const useFleetOperationsGet = /*#__PURE__*/ query<FleetOperation>(op_fleetOperations_get);
