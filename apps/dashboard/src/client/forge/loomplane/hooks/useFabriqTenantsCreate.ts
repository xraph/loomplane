import { mutation } from '@forge-go/client-core';
import { op_fabriqTenants_create } from '../ops/fabriqTenants.create';
import type { Tenant } from '../types';

export const useFabriqTenantsCreate = /*#__PURE__*/ mutation<Tenant, Tenant>(op_fabriqTenants_create);
