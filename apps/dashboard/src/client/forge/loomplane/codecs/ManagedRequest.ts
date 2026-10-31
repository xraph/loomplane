import type { Codec } from '../codec-runtime';

export const codec_ManagedRequest: Codec = {"kind": "object", "fields": {"Group": {"ts": "group"}, "Method": {"ts": "method"}, "Name": {"ts": "name"}, "Namespace": {"ts": "namespace"}, "Resource": {"ts": "resource"}, "ResourceVersion": {"ts": "resourceVersion"}, "UID": {"ts": "uid"}, "Version": {"ts": "version"}}, "required": ["Group", "Method", "Name", "Namespace", "Resource", "UID", "Version"]};
