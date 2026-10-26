import { mutation } from '@forge-go/client-core';
import { op_sources_update } from '../ops/sources.update';
import type { DiscoverySource } from '../types';

export const useSourcesUpdate = /*#__PURE__*/ mutation<DiscoverySource, DiscoverySource>(op_sources_update);
