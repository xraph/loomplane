import type { Codec } from '../codec-runtime';

export const codec_AttachmentPrerequisites: Codec = {"kind": "object", "fields": {"ingress_address": {"ts": "ingressAddress"}, "registry_hostname": {"ts": "registryHostname"}, "require_builds": {"ts": "requireBuilds"}, "require_ingress": {"ts": "requireIngress"}, "require_persistent_storage": {"ts": "requirePersistentStorage"}, "require_tls": {"ts": "requireTls"}, "storage_class": {"ts": "storageClass"}}, "required": ["require_builds", "require_ingress", "require_persistent_storage", "require_tls"]};
