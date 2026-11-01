import { query } from '@forge-go/client-core';
import { op_providerResources_get } from '../ops/providerResources.get';
import type { ProviderResource } from '../types';

export const useProviderResourcesGet = /*#__PURE__*/ query<ProviderResource>(op_providerResources_get);
