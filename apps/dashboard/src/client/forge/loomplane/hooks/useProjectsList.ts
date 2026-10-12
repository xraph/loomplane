import { query } from '@forge-go/client-core';
import { op_projects_list } from '../ops/projects.list';
import type { ProjectList } from '../types';

export const useProjectsList = /*#__PURE__*/ query<ProjectList>(op_projects_list);
