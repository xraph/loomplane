import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_recheck } from '../ops/catalogEndpoints.recheck';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsRecheck = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_recheck);
