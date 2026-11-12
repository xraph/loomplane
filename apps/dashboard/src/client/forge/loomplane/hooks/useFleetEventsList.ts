import { query } from '@forge-go/client-core';
import { op_fleetEvents_list } from '../ops/fleetEvents.list';
import type { EventPage } from '../types';

export const useFleetEventsList = /*#__PURE__*/ query<EventPage>(op_fleetEvents_list);
