import { query } from '@forge-go/client-core';
import { op_domainRoutes_history } from '../ops/domainRoutes.history';
import type { DomainOperationList } from '../types';

export const useDomainRoutesHistory = /*#__PURE__*/ query<DomainOperationList>(op_domainRoutes_history);
