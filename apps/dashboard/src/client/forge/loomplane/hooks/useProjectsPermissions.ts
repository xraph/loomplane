import { query } from '@forge-go/client-core';
import { op_projects_permissions } from '../ops/projects.permissions';
import type { ProjectPermissions } from '../types';

export const useProjectsPermissions = /*#__PURE__*/ query<ProjectPermissions>(op_projects_permissions);
