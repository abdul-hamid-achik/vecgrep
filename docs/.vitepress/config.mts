import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'vecgrep',
  description: 'Local-first semantic code search powered by embeddings.',
  lang: 'en-US',
  cleanUrls: true,
  lastUpdated: true,
  srcDir: '.',
  base: process.env.VITEPRESS_BASE ?? '/',
  head: [
    ['meta', { name: 'theme-color', content: '#0f766e' }],
    ['link', { rel: 'icon', href: '/vecgrep-mark.svg' }]
  ],
  themeConfig: {
    logo: '/vecgrep-mark.svg',
    siteTitle: 'vecgrep',
    search: {
      provider: 'local'
    },
    nav: [
      { text: 'Guide', link: '/quick-start' },
      { text: 'Studio', link: '/studio' },
      { text: 'MCP', link: '/mcp' },
      { text: 'Development', link: '/development' }
    ],
    sidebar: [
      {
        text: 'Get Started',
        items: [
          { text: 'Overview', link: '/' },
          { text: 'Quick Start', link: '/quick-start' },
          { text: 'CLI Usage', link: '/usage' },
          { text: 'Configuration', link: '/configuration' }
        ]
      },
      {
        text: 'Features',
        items: [
          { text: 'Studio', link: '/studio' },
          { text: 'Embedding Providers', link: '/providers' },
          { text: 'MCP Integration', link: '/mcp' },
          { text: 'Docker', link: '/docker' }
        ]
      },
      {
        text: 'Internals',
        items: [
          { text: 'Development Guide', link: '/development' },
          { text: 'VecLite Integration', link: '/veclite-integration' },
          { text: 'codemap Integration', link: '/codemap-integration' }
        ]
      }
    ],
    socialLinks: [
      { icon: 'github', link: 'https://github.com/abdul-hamid-achik/vecgrep' }
    ],
    editLink: {
      pattern: 'https://github.com/abdul-hamid-achik/vecgrep/edit/main/docs/:path',
      text: 'Edit this page'
    },
    footer: {
      message: 'Local-first semantic code search.',
      copyright: 'MIT Licensed'
    }
  }
})
