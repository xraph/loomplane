import { mutation } from '@forge-go/client-core';
import { op_catalogEndpoints_share } from '../ops/catalogEndpoints.share';
import type { CatalogconnectEndpoint } from '../types';

export const useCatalogEndpointsShare = /*#__PURE__*/ mutation<CatalogconnectEndpoint, CatalogconnectEndpoint>(op_catalogEndpoints_share);
