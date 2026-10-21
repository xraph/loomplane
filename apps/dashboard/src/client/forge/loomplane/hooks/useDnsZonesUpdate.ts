import { mutation } from '@forge-go/client-core';
import { op_dnsZones_update } from '../ops/dnsZones.update';
import type { Zone } from '../types';

export const useDnsZonesUpdate = /*#__PURE__*/ mutation<Zone, Zone>(op_dnsZones_update);
