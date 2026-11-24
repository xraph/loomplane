import { query } from '@forge-go/client-core';
import { op_catalogEndpoints_get } from '../ops/catalogEndpoints.get';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsGet = /*#__PURE__*/ query<CatalogconnectEndpoint>(op_catalogEndpoints_get);
