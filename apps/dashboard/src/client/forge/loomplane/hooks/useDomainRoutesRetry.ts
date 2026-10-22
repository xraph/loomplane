import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_retry } from '../ops/domainRoutes.retry';
import type { RouteOperation } from '../types';

export const useDomainRoutesRetry = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_retry);
