import type { Codec } from '../codec-runtime';
import { codec_Assignment } from './Assignment';
import { codec_ChangeInput } from './ChangeInput';
import { codec_CloudprofilesTarget } from './CloudprofilesTarget';
import { codec_DefaultPlacement } from './DefaultPlacement';
import { codec_Preview_blockers } from './Preview.blockers';
import { codec_Preview_environments } from './Preview.environments';

export const codec_Preview: Codec = {"kind": "object", "fields": {"after": {"ts": "after", "codec": () => codec_CloudprofilesTarget}, "after_assignment": {"ts": "afterAssignment", "codec": () => codec_Assignment}, "after_default": {"ts": "afterDefault", "codec": () => codec_DefaultPlacement}, "before": {"ts": "before", "codec": () => codec_CloudprofilesTarget}, "before_assignment": {"ts": "beforeAssignment", "codec": () => codec_Assignment}, "before_default": {"ts": "beforeDefault", "codec": () => codec_DefaultPlacement}, "blockers": {"ts": "blockers", "codec": () => codec_Preview_blockers}, "created_at": {"ts": "createdAt"}, "environments": {"ts": "environments", "codec": () => codec_Preview_environments}, "id": {"ts": "id"}, "input": {"ts": "input", "codec": () => codec_ChangeInput}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "reverts": {"ts": "reverts"}, "revision": {"ts": "revision"}}, "required": ["blockers", "created_at", "environments", "id", "input", "org_id", "project_id", "revision"]};
