import { mutation } from '@forge-go/client-core';
import { op_gitlab_addHost } from '../ops/gitlab.addHost';
import type { GitLabHostView } from '../types';

export const useGitlabAddHost = /*#__PURE__*/ mutation<GitLabHostView, GitLabHostView>(op_gitlab_addHost);
