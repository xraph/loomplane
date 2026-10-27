import { query } from '@forge-go/client-core';
import { op_clouds_usage } from '../ops/clouds.usage';
import type { Usage } from '../types';

export const useCloudsUsage = /*#__PURE__*/ query<Usage>(op_clouds_usage);
