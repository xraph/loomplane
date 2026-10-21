import { mutation } from '@forge-go/client-core';
import { op_dnsZones_verify } from '../ops/dnsZones.verify';
import type { Zone } from '../types';

export const useDnsZonesVerify = /*#__PURE__*/ mutation<Zone, Zone>(op_dnsZones_verify);
