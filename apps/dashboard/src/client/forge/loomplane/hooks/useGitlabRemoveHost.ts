import { mutation } from '@forge-go/client-core';
import { op_gitlab_removeHost } from '../ops/gitlab.removeHost';

export const useGitlabRemoveHost = /*#__PURE__*/ mutation(op_gitlab_removeHost);
