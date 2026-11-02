import { query } from '@forge-go/client-core';
import { op_providerResources_list } from '../ops/providerResources.list';
import type { ProviderResourceList } from '../types';

export const useProviderResourcesList = /*#__PURE__*/ query<ProviderResourceList>(op_providerResources_list);
