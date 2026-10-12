import { mutation } from '@forge-go/client-core';
import { op_projects_create } from '../ops/projects.create';
import type { Project } from '../types';

export const useProjectsCreate = /*#__PURE__*/ mutation<Project, Project>(op_projects_create);
