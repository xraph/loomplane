import { query } from '@forge-go/client-core';
import { op_projects_graph } from '../ops/projects.graph';
import type { ProjectView } from '../types';

export const useProjectsGraph = /*#__PURE__*/ query<ProjectView>(op_projects_graph);
