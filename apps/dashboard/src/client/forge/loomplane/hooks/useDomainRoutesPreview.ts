import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_preview } from '../ops/domainRoutes.preview';
import type { RouteOperation } from '../types';

export const useDomainRoutesPreview = /*#__PURE__*/ mutation<RouteOperation, RouteOperation>(op_domainRoutes_preview);
