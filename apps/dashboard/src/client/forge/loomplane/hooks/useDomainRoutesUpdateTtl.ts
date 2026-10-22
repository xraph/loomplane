import { mutation } from '@forge-go/client-core';
import { op_domainRoutes_updateTTL } from '../ops/domainRoutes.updateTTL';
import type { CloudinfraRoute } from '../types';

export const useDomainRoutesUpdateTtl = /*#__PURE__*/ mutation<CloudinfraRoute, CloudinfraRoute>(op_domainRoutes_updateTTL);
