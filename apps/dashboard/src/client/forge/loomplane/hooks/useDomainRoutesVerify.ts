import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_verify } from '../ops/domainRoutes.verify';
import type { RouteOperation } from '../types';

export const useDomainRoutesVerify = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_verify);
