import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_rollback } from '../ops/domainRoutes.rollback';
import type { RouteOperation } from '../types';

export const useDomainRoutesRollback = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_rollback);
