import { query } from '@forge-go/client-core';
import { op_dnsZones_discover } from '../ops/dnsZones.discover';
import type { ProviderZoneList } from '../types';

export const useDnsZonesDiscover = /*#__PURE__*/ query<ProviderZoneList>(op_dnsZones_discover);
