import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_setAdmin } from '../ops/catalogEndpoints.setAdmin';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsSetAdmin = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_setAdmin);
