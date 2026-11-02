import { mutation } from '@forge-go/client-core';
import { op_sources_create } from '../ops/sources.create';
import type { DiscoverySource } from '../types';

export const useSourcesCreate = /*#__PURE__*/ mutation<DiscoverySource, DiscoverySource>(op_sources_create);
