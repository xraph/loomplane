import { query } from '@forge-go/client-core';
import { op_aiModelDeployments_list } from '../ops/aiModelDeployments.list';
import type { ModelDeploymentList } from '../types';

export const useAiModelDeploymentsList = /*#__PURE__*/ query<ModelDeploymentList>(op_aiModelDeployments_list);
