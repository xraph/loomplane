import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_cutover } from '../ops/domainRoutes.cutover';
import type { RouteOperation } from '../types';

export const useDomainRoutesCutover = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_cutover);
