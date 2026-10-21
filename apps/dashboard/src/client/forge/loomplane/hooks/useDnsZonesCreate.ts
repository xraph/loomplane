import { mutation } from '@forge-go/client-core';
import { op_dnsZones_create } from '../ops/dnsZones.create';
import type { Zone } from '../types';

export const useDnsZonesCreate = /*#__PURE__*/ mutation<Zone, Zone>(op_dnsZones_create);
