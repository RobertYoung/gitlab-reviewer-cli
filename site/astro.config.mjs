// @ts-check
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';

const repo = 'https://github.com/RobertYoung/gitlab-reviewer-cli';

export default defineConfig({
	site: 'https://robertyoung.github.io',
	base: '/gitlab-reviewer-cli',
	trailingSlash: 'always',
	// Screenshots ship as-is: skipping sharp keeps the build free of native dependencies.
	image: { service: passthroughImageService() },
	integrations: [
		starlight({
			title: 'gitlab-reviewer',
			description: 'Review GitLab merge requests with Claude: focused review agents on the real checkout, human triage, inline comments.',
			logo: { src: './src/assets/logo.svg', alt: '' },
			social: [{ icon: 'github', label: 'GitHub', href: repo }],
			editLink: { baseUrl: `${repo}/edit/main/site/` },
			customCss: ['./src/styles/theme.css'],
			components: { Hero: './src/components/Hero.astro', SocialIcons: './src/components/SocialIcons.astro' },
			expressiveCode: {
				themes: ['github-dark', 'github-light'],
				styleOverrides: { borderRadius: '8px' },
			},
			sidebar: [
				{ label: 'Start', items: ['getting-started'] },
				{
					label: 'Guides',
					items: ['tui', 'gui', 'headless', 'agent-driven', 'review-agents', 'example-agents', 'publishing', 'checkout-modes', 'mcp-servers', 'recipes'],
				},
				{
					label: 'Reference',
					items: ['reference/configuration', 'reference/cli', 'security-model', 'troubleshooting'],
				},
			],
		}),
	],
});
