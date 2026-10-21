import { query } from '@forge-go/client-core';
import { op_domainRoutes_list } from '../ops/domainRoutes.list';
import type { DomainRouteList } from '../types';

export const useDomainRoutesList = /*#__PURE__*/ query<DomainRouteList>(op_domainRoutes_list);
