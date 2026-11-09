import { query } from '@forge-go/client-core';
import { op_workspacePlatformCloud_get } from '../ops/workspacePlatformCloud.get';
import type { WorkspaceView } from '../types';

export const useWorkspacePlatformCloudGet = /*#__PURE__*/ query<WorkspaceView>(op_workspacePlatformCloud_get);
