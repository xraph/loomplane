import type { Codec } from '../codec-runtime';
import { codec_ComponentConfig_dependencies } from './ComponentConfig.dependencies';
import { codec_ResolvedComponent } from './ResolvedComponent';

export const codec_ComponentConfig: Codec = {"kind": "object", "fields": {"component": {"ts": "component", "codec": () => codec_ResolvedComponent}, "dependencies": {"ts": "dependencies", "codec": () => codec_ComponentConfig_dependencies}, "inputs": {"ts": "inputs"}, "kind": {"ts": "kind"}, "protection": {"ts": "protection"}, "resource": {"ts": "resource"}, "revision": {"ts": "revision"}}, "required": ["component", "kind", "protection", "resource", "revision"]};
