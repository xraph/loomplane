import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_apply } from '../ops/domainRoutes.apply';
import type { RouteOperation } from '../types';

export const useDomainRoutesApply = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_apply);
