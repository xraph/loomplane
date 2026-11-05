import { mutation } from '@forge-go/client-core';
import { op_cloudLocations_verify } from '../ops/cloudLocations.verify';
import type { CloudLocation } from '../types';

export const useCloudLocationsVerify = /*#__PURE__*/ mutation<CloudLocation, CloudLocation>(op_cloudLocations_verify);
