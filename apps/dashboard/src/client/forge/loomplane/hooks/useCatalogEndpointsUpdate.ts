import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_update } from '../ops/catalogEndpoints.update';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsUpdate = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_update);
