import { query } from '@forge-go/client-core';
import { op_dnsZones_list } from '../ops/dnsZones.list';
import type { ZoneList } from '../types';

export const useDnsZonesList = /*#__PURE__*/ query<ZoneList>(op_dnsZones_list);
