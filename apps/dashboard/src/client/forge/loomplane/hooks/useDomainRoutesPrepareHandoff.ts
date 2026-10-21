import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_prepareHandoff } from '../ops/domainRoutes.prepareHandoff';
import type { RouteOperation } from '../types';

export const useDomainRoutesPrepareHandoff = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_prepareHandoff);
