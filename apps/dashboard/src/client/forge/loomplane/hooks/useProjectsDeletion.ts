import { query } from '@forge-go/client-core';
import { op_projects_deletion } from '../ops/projects.deletion';
import type { DeletionPreview } from '../types';

export const useProjectsDeletion = /*#__PURE__*/ query<DeletionPreview>(op_projects_deletion);
