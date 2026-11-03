import { mutation } from '@forge-go/client-core';
import { op_gitlab_disconnect } from '../ops/gitlab.disconnect';

export const useGitlabDisconnect = /*#__PURE__*/ mutation(op_gitlab_disconnect);
