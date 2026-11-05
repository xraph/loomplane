import { query } from '@forge-go/client-core';
import { op_fleetInstances_get } from '../ops/fleetInstances.get';
import type { Instance } from '../types';

export const useFleetInstancesGet = /*#__PURE__*/ query<Instance>(op_fleetInstances_get);
