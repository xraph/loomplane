import { mutation } from '@forge-go/client-core';
import { op_cloudLocations_save } from '../ops/cloudLocations.save';
import type { CloudLocation } from '../types';

export const useCloudLocationsSave = /*#__PURE__*/ mutation<CloudLocation, CloudLocation>(op_cloudLocations_save);
