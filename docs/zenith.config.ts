import { defineConfig } from 'zenith-docs/config';

// Same origin as the site, which this build is copied into under /docs.
const site = process.env.SITE_URL || 'https://quasar.sh';

export default defineConfig({
  site,
  base: '/docs',
  title: 'Quasar Documentation',
  logo: { src: './theme/logo.svg', alt: 'Quasar' },
  description: 'Documentation for Quasar, a self-hosted mini-PaaS in Go.',
  github: 'https://github.com/AymericChaverot/quasar',
  links: [{ label: 'Website', href: site }],
  customCss: ['./theme/accent.css', './theme/icons.css'],
  openapi: {
    quasar: './openapi/quasar.yaml',
  },
});
