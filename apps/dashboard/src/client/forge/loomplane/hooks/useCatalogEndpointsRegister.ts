import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_register } from '../ops/catalogEndpoints.register';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsRegister = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_register);
