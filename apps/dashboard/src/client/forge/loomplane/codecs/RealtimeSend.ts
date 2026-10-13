import type { Codec } from '../codec-runtime';
import { codec_RealtimeSend_deployment_ids } from './RealtimeSend.deployment_ids';
import { codec_RealtimeSend_project_ids } from './RealtimeSend.project_ids';

export const codec_RealtimeSend: Codec = {"kind": "object", "fields": {"cursor": {"ts": "cursor"}, "deployment_ids": {"ts": "deploymentIds", "codec": () => codec_RealtimeSend_deployment_ids}, "op": {"ts": "op"}, "org_id": {"ts": "orgId"}, "project_ids": {"ts": "projectIds", "codec": () => codec_RealtimeSend_project_ids}}, "required": ["op", "org_id"]};
