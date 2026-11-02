import { mutation } from '@forge-go/client-core';
import { op_projects_include } from '../ops/projects.include';
import type { Project } from '../types';

export const useProjectsInclude = /*#__PURE__*/ mutation<Project, Project>(op_projects_include);
