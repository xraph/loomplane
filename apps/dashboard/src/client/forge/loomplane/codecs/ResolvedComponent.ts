import type { Codec } from '../codec-runtime';
import { codec_HTTPProbe } from './HTTPProbe';
import { codec_ResolvedComponent_after } from './ResolvedComponent.after';
import { codec_ResolvedComponent_args } from './ResolvedComponent.args';
import { codec_ResolvedComponent_command } from './ResolvedComponent.command';
import { codec_ResolvedComponent_env } from './ResolvedComponent.env';
import { codec_ResolvedComponent_files } from './ResolvedComponent.files';
import { codec_ResolvedComponent_kubernetes_rules } from './ResolvedComponent.kubernetes_rules';
import { codec_ResolvedComponent_ports } from './ResolvedComponent.ports';
import { codec_Resources } from './Resources';
import { codec_Volume } from './Volume';

export const codec_ResolvedComponent: Codec = {"kind": "object", "fields": {"after": {"ts": "after", "codec": () => codec_ResolvedComponent_after}, "args": {"ts": "args", "codec": () => codec_ResolvedComponent_args}, "command": {"ts": "command", "codec": () => codec_ResolvedComponent_command}, "env": {"ts": "env", "codec": () => codec_ResolvedComponent_env}, "files": {"ts": "files", "codec": () => codec_ResolvedComponent_files}, "image": {"ts": "image"}, "kubernetes_rules": {"ts": "kubernetesRules", "codec": () => codec_ResolvedComponent_kubernetes_rules}, "name": {"ts": "name"}, "ports": {"ts": "ports", "codec": () => codec_ResolvedComponent_ports}, "protection": {"ts": "protection"}, "readiness": {"ts": "readiness", "codec": () => codec_HTTPProbe}, "requires": {"ts": "requires"}, "resources": {"ts": "resources", "codec": () => codec_Resources}, "role": {"ts": "role"}, "step": {"ts": "step"}, "timeout": {"ts": "timeout"}, "volume": {"ts": "volume", "codec": () => codec_Volume}}, "required": ["image", "name", "resources", "role", "step"]};
