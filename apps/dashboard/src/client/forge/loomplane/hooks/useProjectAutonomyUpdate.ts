import { mutation } from '@forge-go/client-core';
import { op_projectAutonomy_update } from '../ops/projectAutonomy.update';
import type { Project } from '../types';

export const useProjectAutonomyUpdate = /*#__PURE__*/ mutation<Project, Project>(op_projectAutonomy_update);
