import type { Codec } from '../codec-runtime';
import { codec_Graph_build_systems } from './Graph.build_systems';
import { codec_Graph_edges } from './Graph.edges';
import { codec_Graph_notes } from './Graph.notes';
import { codec_Graph_packages } from './Graph.packages';
import { codec_Graph_resources } from './Graph.resources';
import { codec_Graph_services } from './Graph.services';
import { codec_Graph_sources } from './Graph.sources';
import { codec_Graph_workspaces } from './Graph.workspaces';

export const codec_Graph: Codec = {"kind": "object", "fields": {"build_systems": {"ts": "buildSystems", "codec": () => codec_Graph_build_systems}, "edges": {"ts": "edges", "codec": () => codec_Graph_edges}, "notes": {"ts": "notes", "codec": () => codec_Graph_notes}, "packages": {"ts": "packages", "codec": () => codec_Graph_packages}, "resources": {"ts": "resources", "codec": () => codec_Graph_resources}, "services": {"ts": "services", "codec": () => codec_Graph_services}, "sources": {"ts": "sources", "codec": () => codec_Graph_sources}, "version": {"ts": "version"}, "workspaces": {"ts": "workspaces", "codec": () => codec_Graph_workspaces}}, "required": ["build_systems", "edges", "notes", "packages", "resources", "services", "version", "workspaces"]};
