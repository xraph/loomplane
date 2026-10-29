import type { Codec } from '../codec-runtime';

export const codec_Preferences: Codec = {"kind": "object", "fields": {"agent_email": {"ts": "agentEmail"}, "deployment_email": {"ts": "deploymentEmail"}, "mail_enabled": {"ts": "mailEnabled"}}, "required": ["agent_email", "deployment_email", "mail_enabled"]};
