import { query } from '@forge-go/client-core';
import { op_releaseWatches_forDeployment } from '../ops/releaseWatches.forDeployment';
import type { Watch } from '../types';

export const useReleaseWatchesForDeployment = /*#__PURE__*/ query<Watch>(op_releaseWatches_forDeployment);
