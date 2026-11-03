import { mutation } from '@forge-go/client-core';
import { op_gitlab_webhookSecret } from '../ops/gitlab.webhookSecret';

export const useGitlabWebhookSecret = /*#__PURE__*/ mutation(op_gitlab_webhookSecret);
