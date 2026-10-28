import { mutation } from '@forge-go/client-core';
import { op_projects_exclude } from '../ops/projects.exclude';
import type { Project } from '../types';

export const useProjectsExclude = /*#__PURE__*/ mutation<Project, Project>(op_projects_exclude);
