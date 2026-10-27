import { query } from '@forge-go/client-core';
import { op_domainRoutes_status } from '../ops/domainRoutes.status';
import type { CloudinfraRouteStatus } from '../types';

export const useDomainRoutesStatus = /*#__PURE__*/ query<CloudinfraRouteStatus>(op_domainRoutes_status);
