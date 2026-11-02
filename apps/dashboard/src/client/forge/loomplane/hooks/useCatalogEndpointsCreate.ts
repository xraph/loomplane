import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_create } from '../ops/catalogEndpoints.create';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsCreate = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_create);
