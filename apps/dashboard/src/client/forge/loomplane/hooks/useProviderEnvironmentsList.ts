import { query } from '@forge-go/client-core';
import { op_providerEnvironments_list } from '../ops/providerEnvironments.list';
import type { ProviderEnvironmentList } from '../types';

export const useProviderEnvironmentsList = /*#__PURE__*/ query<ProviderEnvironmentList>(op_providerEnvironments_list);
