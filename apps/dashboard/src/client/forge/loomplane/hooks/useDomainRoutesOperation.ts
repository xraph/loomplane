import { query } from '@forge-go/client-core';
import { op_domainRoutes_operation } from '../ops/domainRoutes.operation';
import type { RouteOperation } from '../types';

export const useDomainRoutesOperation = /*#__PURE__*/ query<RouteOperation>(op_domainRoutes_operation);
