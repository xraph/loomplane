import { query } from '@forge-go/client-core';
import { op_dnsZones_get } from '../ops/dnsZones.get';
import type { DomainDetail } from '../types';

export const useDnsZonesGet = /*#__PURE__*/ query<DomainDetail>(op_dnsZones_get);
