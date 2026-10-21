import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_remove } from '../ops/domainRoutes.remove';
import type { RouteOperation } from '../types';

export const useDomainRoutesRemove = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_remove);
