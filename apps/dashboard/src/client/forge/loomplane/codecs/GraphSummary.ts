import type { Codec } from '../codec-runtime';

export const codec_GraphSummary: Codec = {"kind": "object", "fields": {"deployable": {"ts": "deployable"}, "needs_confirmation": {"ts": "needsConfirmation"}, "packages": {"ts": "packages"}, "resources": {"ts": "resources"}, "services": {"ts": "services"}}, "required": ["deployable", "needs_confirmation", "packages", "resources", "services"]};
