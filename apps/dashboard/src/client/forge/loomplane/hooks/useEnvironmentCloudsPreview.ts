import { mutation } from '@forge-go/client-core';
import { op_environmentClouds_preview } from '../ops/environmentClouds.preview';
import type { Preview } from '../types';

export const useEnvironmentCloudsPreview = /*#__PURE__*/ mutation<Preview, Preview>(op_environmentClouds_preview);
