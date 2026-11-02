import { mutation } from '@forge-go/client-core';
import { op_providerResources_connect } from '../ops/providerResources.connect';
import type { ProviderResource } from '../types';

export const useProviderResourcesConnect = /*#__PURE__*/ mutation<ProviderResource, ProviderResource>(op_providerResources_connect);
