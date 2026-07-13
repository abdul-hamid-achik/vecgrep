import { defineConfig } from 'vitepress'

const canonicalBase = 'https://vecgrep.dev'

const pageMeta: Record<string, { title: string; description: string }> = {
  '/': {
    title: 'vecgrep — Local-first semantic code search',
    description: 'Index codebases, search with natural language, and connect AI assistants via MCP. Powered by Ollama, local-first by default.'
  },
  '/quick-start': {
    title: 'Quick Start — vecgrep',
    description: 'Build vecgrep, register a project, index it, and run your first semantic search in under 60 seconds.'
  },
  '/usage': {
    title: 'CLI Usage — vecgrep',
    description: 'Complete CLI reference: init, index, search, similar, status, memory, and shell completion commands.'
  },
  '/configuration': {
    title: 'Configuration — vecgrep',
    description: 'Hierarchical config resolution, project files, environment variables, and embedding provider settings.'
  },
  '/studio': {
    title: 'Studio TUI — vecgrep',
    description: 'Full-screen terminal workspace built with Bubble Tea. Search, preview code, index projects, and inspect vector status.'
  },
  '/providers': {
    title: 'Embedding Providers — vecgrep',
    description: 'Ollama, OpenAI, Cohere, and Voyage AI provider configuration. Local presets, dimensions, and re-indexing rules.'
  },
  '/mcp': {
    title: 'MCP Integration — vecgrep',
    description: 'Model Context Protocol server for AI assistants. 11 tools for semantic search, indexing, and codebase inspection.'
  },
  '/docker': {
    title: 'Docker — vecgrep',
    description: 'Run vecgrep in Docker with Ollama. Container setup and configuration.'
  },
  '/development': {
    title: 'Development Guide — vecgrep',
    description: 'Architecture, embedding flow, database, MCP server, and contribution guide for vecgrep developers.'
  },
  '/veclite-integration': {
    title: 'VecLite Integration — vecgrep',
    description: 'How vecgrep uses VecLite for HNSW vector storage, metadata payloads, and incremental indexing.'
  },
  '/codemap-integration': {
    title: 'codemap Integration — vecgrep',
    description: 'Symbol impact analysis and blast-radius scoping via codemap daemon integration.'
  }
}

export default defineConfig({
  title: 'vecgrep',
  description: 'Local-first semantic code search powered by vector embeddings. Index codebases, search with natural language, and connect AI assistants via MCP.',
  lang: 'en-US',
  cleanUrls: true,
  lastUpdated: true,
  srcDir: '.',
  base: process.env.VITEPRESS_BASE ?? '/',
  sitemap: {
    hostname: canonicalBase
  },
  head: [
    // Theme color
    ['meta', { name: 'theme-color', content: '#0f766e' }],

    // Favicon
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/vecgrep-mark.svg' }],
    ['link', { rel: 'apple-touch-icon', href: '/vecgrep-mark.svg' }],

    // Open Graph (defaults — per-page overrides via transformHead)
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: 'vecgrep' }],
    ['meta', { property: 'og:image', content: canonicalBase + '/og-image.png' }],
    ['meta', { property: 'og:image:width', content: '1200' }],
    ['meta', { property: 'og:image:height', content: '630' }],
    ['meta', { property: 'og:image:alt', content: 'vecgrep — Local-first semantic code search' }],
    ['meta', { property: 'og:locale', content: 'en_US' }],

    // Twitter Card (defaults — per-page overrides via transformHead)
    ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
    ['meta', { name: 'twitter:image', content: canonicalBase + '/og-image.png' }],
    ['meta', { name: 'twitter:image:alt', content: 'vecgrep — Local-first semantic code search' }],

    // Keywords
    ['meta', { name: 'keywords', content: 'semantic code search, vector search, code indexing, embeddings, ollama, mcp, model context protocol, local-first, ai code assistant, codebase search, developer tools' }],

    // Author
    ['meta', { name: 'author', content: 'Abdul Hamid Achik' }],

    // Structured data: SoftwareApplication
    ['script', { type: 'application/ld+json' }, JSON.stringify({
      '@context': 'https://schema.org',
      '@type': 'SoftwareApplication',
      'name': 'vecgrep',
      'description': 'Local-first semantic code search powered by vector embeddings. Index codebases, search with natural language, and connect AI assistants via MCP.',
      'applicationCategory': 'DeveloperApplication',
      'operatingSystem': 'Cross-platform',
      'url': canonicalBase,
      'downloadUrl': 'https://github.com/abdul-hamid-achik/vecgrep/releases',
      'codeRepository': 'https://github.com/abdul-hamid-achik/vecgrep',
      'license': 'https://github.com/abdul-hamid-achik/vecgrep/blob/main/LICENSE',
      'programmingLanguage': 'Go',
      'offers': {
        '@type': 'Offer',
        'price': '0',
        'priceCurrency': 'USD'
      },
      'featureList': [
        'Semantic vector search with Ollama embeddings',
        'Hybrid search combining semantic and BM25 keyword matching',
        'MCP server for AI assistant integration',
        'Studio TUI built with Bubble Tea',
        'Incremental indexing with file-hash detection',
        'Language-aware code chunking',
        'OpenAI, Cohere, and Voyage AI provider support'
      ]
    })],

    // Structured data: Organization
    ['script', { type: 'application/ld+json' }, JSON.stringify({
      '@context': 'https://schema.org',
      '@type': 'Organization',
      'name': 'vecgrep',
      'url': canonicalBase,
      'logo': canonicalBase + '/vecgrep-mark.svg',
      'sameAs': [
        'https://github.com/abdul-hamid-achik/vecgrep'
      ]
    })],

    // Structured data: FAQPage (only on homepage, injected via head)
    ['script', { type: 'application/ld+json' }, JSON.stringify({
      '@context': 'https://schema.org',
      '@type': 'FAQPage',
      'mainEntity': [
        {
          '@type': 'Question',
          'name': 'Do I need a GPU or special hardware to run vecgrep?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'No. vecgrep uses Ollama with nomic-embed-text by default, which runs efficiently on CPU. Embedding generation is a one-time cost per indexing run — search itself is pure vector similarity and is instant on any machine.'
          }
        },
        {
          '@type': 'Question',
          'name': 'How is vecgrep different from grep or ripgrep?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'grep and ripgrep find exact text patterns. vecgrep finds semantically related code — you describe what you are looking for in natural language and get results that match the meaning, not just the text. The hybrid mode also blends BM25 keyword matching so you never lose exact-match capability.'
          }
        },
        {
          '@type': 'Question',
          'name': 'Does my source code get sent to the cloud?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'No. With the default Ollama provider, embeddings are generated locally and vectors are stored under ~/.vecgrep/projects/. Nothing leaves your machine. Cloud providers (OpenAI, Cohere, Voyage AI) are optional and only activated when you explicitly configure them.'
          }
        },
        {
          '@type': 'Question',
          'name': 'Which AI assistants work with the MCP server?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'Any client that supports the Model Context Protocol, including Claude Code, Cursor, and custom MCP-compatible clients. The server exposes 11 tools for searching, indexing, status inspection, similar-code discovery, and codebase overview.'
          }
        },
        {
          '@type': 'Question',
          'name': 'What languages does vecgrep support?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'vecgrep chunker is language-aware and supports Go, JavaScript, TypeScript, Python, Rust, Java, C/C++, and more. Code is chunked by syntax structures (functions, classes, methods) rather than arbitrary line splits.'
          }
        },
        {
          '@type': 'Question',
          'name': 'How does incremental indexing work?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'vecgrep hashes every file on each vecgrep index run. Files whose hash has not changed are skipped — only new or modified files get re-embedded. A full rebuild (vecgrep index --full) is only needed when you change embedding model, dimensions, or chunking profile.'
          }
        },
        {
          '@type': 'Question',
          'name': 'Is vecgrep free and open source?',
          'acceptedAnswer': {
            '@type': 'Answer',
            'text': 'Yes. vecgrep is MIT-licensed and available on GitHub. Contributions, issues, and feature requests are welcome.'
          }
        }
      ]
    })]
  ],

  // Per-page canonical URLs, OG titles, and descriptions
  transformHead: (context) => {
    if (!context.page) return []
    const pageSlug = context.page.replace(/\.md$/, '')
    const pagePath = pageSlug === 'index' ? '/' : '/' + pageSlug
    const meta = pageMeta[pagePath] ?? {
      title: context.title,
      description: context.description
    }
    const canonicalUrl = canonicalBase + pagePath

    return [
      ['link', { rel: 'canonical', href: canonicalUrl }],
      ['meta', { property: 'og:title', content: meta.title }],
      ['meta', { property: 'og:description', content: meta.description }],
      ['meta', { property: 'og:url', content: canonicalUrl }],
      ['meta', { name: 'twitter:title', content: meta.title }],
      ['meta', { name: 'twitter:description', content: meta.description }]
    ]
  },

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
      { text: 'Providers', link: '/providers' },
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