import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_releaseHandoff } from '../ops/domainRoutes.releaseHandoff';
import type { RouteOperation } from '../types';

export const useDomainRoutesReleaseHandoff = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_releaseHandoff);
