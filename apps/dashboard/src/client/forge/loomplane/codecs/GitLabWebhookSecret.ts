import type { Codec } from '../codec-runtime';

export const codec_GitLabWebhookSecret: Codec = {"kind": "object", "fields": {"secret_token": {"ts": "secretToken"}, "signing_token": {"ts": "signingToken"}, "url": {"ts": "url"}}, "required": ["secret_token", "signing_token", "url"]};
