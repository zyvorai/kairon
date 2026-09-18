import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Kairon',
  tagline: 'Real VMs on Kubernetes — without KubeVirt',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://zyvorai.github.io',
  baseUrl: '/kairon/',

  organizationName: 'zyvorai',
  projectName: 'kairon',

  onBrokenLinks: 'warn',

  markdown: {
    format: 'md',
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: 'docs',
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/zyvorai/kairon/tree/main/docs/',
          exclude: ['**/assets/**'],
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/social-preview.png',
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Kairon',
      logo: {
        alt: 'Kairon',
        src: 'img/favicon.svg',
      },
      hideOnScroll: false,
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'right',
          label: 'Docs',
        },
        {
          href: 'https://github.com/zyvorai/kairon',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'Getting started', to: '/docs/getting-started'},
            {label: 'What ships', to: '/docs/WHAT_SHIPS'},
            {label: 'Status', to: '/docs/STATUS'},
            {label: 'CLI', to: '/docs/CLI'},
            {label: 'Architecture', to: '/docs/architecture'},
            {label: 'Network Fabric', to: '/docs/network-fabric'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'GitHub', href: 'https://github.com/zyvorai/kairon'},
            {label: 'Roadmap', href: 'https://github.com/zyvorai/kairon/blob/main/ROADMAP.md'},
            {label: 'License (Apache-2.0)', href: 'https://github.com/zyvorai/kairon/blob/main/LICENSE'},
          ],
        },
        {
          title: 'Zyvor Enterprise',
          items: [
            {label: 'zyvor.dev', href: 'https://zyvor.dev'},
            {label: 'sales@zyvor.dev', href: 'mailto:sales@zyvor.dev'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Zyvor. Kairon core is Apache-2.0 licensed.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
