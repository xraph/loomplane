import { query } from '@forge-go/client-core';
import { op_fabriqTenants_list } from '../ops/fabriqTenants.list';
import type { TenantList } from '../types';

export const useFabriqTenantsList = /*#__PURE__*/ query<TenantList>(op_fabriqTenants_list);
