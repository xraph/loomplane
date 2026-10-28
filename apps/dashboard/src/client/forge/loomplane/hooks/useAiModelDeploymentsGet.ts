import { query } from '@forge-go/client-core';
import { op_aiModelDeployments_get } from '../ops/aiModelDeployments.get';
import type { ModelsStatus } from '../types';

export const useAiModelDeploymentsGet = /*#__PURE__*/ query<ModelsStatus>(op_aiModelDeployments_get);
