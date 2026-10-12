import { mutation } from '@forge-go/client-core';
import { op_github_connect } from '../ops/github.connect';
import type { GitHubInstallation } from '../types';

export const useGithubConnect = /*#__PURE__*/ mutation<GitHubInstallation, GitHubInstallation>(op_github_connect);
