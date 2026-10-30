import { defineConfig } from 'zenith-docs/config';

// Same origin as the site, which this build is copied into under /docs.
const site = process.env.SITE_URL || 'https://quasar.sh';

export default defineConfig({
  site,
  base: '/docs',
  title: 'Quasar Documentation',
  logo: { src: './theme/logo.svg', alt: 'Quasar' },
  // The site's chartreuse as is on dark backgrounds; darkened on light ones, where
  // #d4e815 would not be readable as link text.
  accent: { dark: '#d4e815', light: 'oklch(52% 0.13 115)' },
  description: 'Documentation for Quasar, a self-hosted mini-PaaS in Go.',
  github: 'https://github.com/AymericChaverot/quasar',
  links: [{ label: 'Website', href: site }],
  customCss: ['./theme/icons.css'],
  // The logo with its colors written out: social images can't resolve the
  // stylesheet variables the site's logo relies on.
  og: { logo: { src: './theme/og-logo.svg' } },
  // The site publishes these pages on every tag, so a broken link stops the build.
  linkValidation: 'error',
  openapi: {
    quasar: './openapi/quasar.yaml',
  },
});
