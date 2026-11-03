import { mutation } from '@forge-go/client-core';
import { op_gitlab_connect } from '../ops/gitlab.connect';
import type { GitLabConnectionView } from '../types';

export const useGitlabConnect = /*#__PURE__*/ mutation<GitLabConnectionView, GitLabConnectionView>(op_gitlab_connect);
