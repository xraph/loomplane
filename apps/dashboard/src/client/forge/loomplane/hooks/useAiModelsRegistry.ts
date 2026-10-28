import { query } from '@forge-go/client-core';
import { op_aiModels_registry } from '../ops/aiModels.registry';
import type { RegistryList } from '../types';

export const useAiModelsRegistry = /*#__PURE__*/ query<RegistryList>(op_aiModels_registry);
