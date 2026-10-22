import { query } from '@forge-go/client-core';
import { op_domainRoutes_status } from '../ops/domainRoutes.status';
import type { RouteStatus } from '../types';

export const useDomainRoutesStatus = /*#__PURE__*/ query<RouteStatus>(op_domainRoutes_status);
