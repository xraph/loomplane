import { mutation } from '@forge-go/client-core';
import { op_providerResources_sync } from '../ops/providerResources.sync';
import type { ProviderResourceSync } from '../types';

export const useProviderResourcesSync = /*#__PURE__*/ mutation<ProviderResourceSync, ProviderResourceSync>(op_providerResources_sync);
