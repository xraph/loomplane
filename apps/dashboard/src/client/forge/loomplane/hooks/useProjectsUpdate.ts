import { mutation } from '@forge-go/client-core';
import { op_projects_update } from '../ops/projects.update';
import type { Project } from '../types';

export const useProjectsUpdate = /*#__PURE__*/ mutation<Project, Project>(op_projects_update);
