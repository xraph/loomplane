import { query } from '@forge-go/client-core';
import { op_providers_list } from '../ops/providers.list';
import type { ProviderList } from '../types';

export const useProvidersList = /*#__PURE__*/ query<ProviderList>(op_providers_list);
