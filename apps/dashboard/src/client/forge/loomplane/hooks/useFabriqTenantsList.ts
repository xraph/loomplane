import { query } from '@forge-go/client-core';
import { op_fabriqTenants_list } from '../ops/fabriqTenants.list';
import type { PlanningTenantList } from '../types';

export const useFabriqTenantsList = /*#__PURE__*/ query<PlanningTenantList>(op_fabriqTenants_list);
