import { query } from '@forge-go/client-core';
import { op_octopus_discovery } from '../ops/octopus.discovery';
import type { DiscoverySummary } from '../types';

export const useOctopusDiscovery = /*#__PURE__*/ query<DiscoverySummary>(op_octopus_discovery);
