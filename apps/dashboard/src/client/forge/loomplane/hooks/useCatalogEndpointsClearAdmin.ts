import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_clearAdmin } from '../ops/catalogEndpoints.clearAdmin';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsClearAdmin = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_clearAdmin);
