import { mutation } from '@forge-go/client-core';
import { op_projects_setTeam } from '../ops/projects.setTeam';
import type { Project } from '../types';

export const useProjectsSetTeam = /*#__PURE__*/ mutation<Project, Project>(op_projects_setTeam);
