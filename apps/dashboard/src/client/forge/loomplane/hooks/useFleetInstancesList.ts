import { query } from '@forge-go/client-core';
import { op_fleetInstances_list } from '../ops/fleetInstances.list';
import type { InstancePage } from '../types';

export const useFleetInstancesList = /*#__PURE__*/ query<InstancePage>(op_fleetInstances_list);
