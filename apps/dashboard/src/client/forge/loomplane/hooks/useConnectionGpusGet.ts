import { query } from '@forge-go/client-core';
import { op_connectionGpus_get } from '../ops/connectionGpus.get';
import type { GPUInventory } from '../types';

export const useConnectionGpusGet = /*#__PURE__*/ query<GPUInventory>(op_connectionGpus_get);
