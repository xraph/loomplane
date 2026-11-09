import { mutation } from '@forge-go/client-core';
import { op_fabriqTenants_create } from '../ops/fabriqTenants.create';
import type { FabriqloomTenant } from '../types';

export const useFabriqTenantsCreate = /*#__PURE__*/ mutation<FabriqloomTenant, FabriqloomTenant>(op_fabriqTenants_create);
