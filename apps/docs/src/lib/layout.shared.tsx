import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { Wordmark } from '@/components/landing/wordmark';
import { gitConfig } from './shared';

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: <Wordmark className="docs-wordmark" />,
    },
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
    links: [
      { text: 'Platform', url: '/platform' },
      { text: 'Clouds', url: '/clouds' },
      { text: 'Documentation', url: '/docs', active: 'nested-url' },
    ],
  };
}
