import type { Codec } from '../codec-runtime';
import { codec_Action_changes } from './Action.changes';
import { codec_Action_depends_on } from './Action.depends_on';
import { codec_ResourceConfig } from './ResourceConfig';
import { codec_ServiceConfig } from './ServiceConfig';

export const codec_Action: Codec = {"kind": "object", "fields": {"changes": {"ts": "changes", "codec": () => codec_Action_changes}, "connection_id": {"ts": "connectionId"}, "creates": {"ts": "creates"}, "depends_on": {"ts": "dependsOn", "codec": () => codec_Action_depends_on}, "engine": {"ts": "engine"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "op": {"ts": "op"}, "provider": {"ts": "provider"}, "reason": {"ts": "reason"}, "region": {"ts": "region"}, "resource": {"ts": "resource", "codec": () => codec_ResourceConfig}, "service": {"ts": "service", "codec": () => codec_ServiceConfig}, "step": {"ts": "step"}, "subject": {"ts": "subject"}}, "required": ["creates", "engine", "id", "name", "op", "provider", "reason", "step", "subject"]};
