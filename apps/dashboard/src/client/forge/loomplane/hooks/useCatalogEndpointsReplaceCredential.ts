import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_replaceCredential } from '../ops/catalogEndpoints.replaceCredential';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsReplaceCredential = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_replaceCredential);
