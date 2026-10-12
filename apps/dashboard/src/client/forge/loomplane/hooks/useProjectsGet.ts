import { query } from '@forge-go/client-core';
import { op_projects_get } from '../ops/projects.get';
import type { Project } from '../types';

export const useProjectsGet = /*#__PURE__*/ query<Project>(op_projects_get);
