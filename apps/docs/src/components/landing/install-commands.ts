// The two ways in: the server image, and the CLI.
export const installCommands = [
  { id: 'docker', kind: 'Server', label: 'Copy the Docker command', text: 'docker pull ghcr.io/xraph/loomplane:latest' },
  { id: 'brew', kind: 'CLI', label: 'Copy the Homebrew command', text: 'brew install xraph/tap/loomctl' },
] as const;

export const installGuideHref = '/docs/quick-start';
