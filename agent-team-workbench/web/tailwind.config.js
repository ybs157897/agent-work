/**
 * 主题 token 静态移植自原型 prototype/src/data/theme.json（单主题）。
 * 颜色以 CSS 变量通道形式定义（--color-* 在 index.css :root），
 * 与原型 tailwind.config.ts 生成的 hsl(var(--color-*) / <alpha-value>) 等价。
 */
const withVar = (name) => `hsl(var(--color-${name}) / <alpha-value>)`;

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx,css}'],
  theme: {
    extend: {
      colors: {
        'brand-primary': withVar('brand-primary'),
        'brand-accent': withVar('brand-accent'),
        'surface-base': withVar('surface-base'),
        'surface-warm': withVar('surface-warm'),
        'surface-raised': withVar('surface-raised'),
        'surface-glass': withVar('surface-glass'),
        'text-primary': withVar('text-primary'),
        'text-secondary': withVar('text-secondary'),
        'text-tertiary': withVar('text-tertiary'),
        'text-muted': withVar('text-muted'),
        'text-inverse': withVar('text-inverse'),
        'border-subtle': withVar('border-subtle'),
        'border-strong': withVar('border-strong'),
        'status-success': withVar('status-success'),
        'status-warning': withVar('status-warning'),
        'status-error': withVar('status-error'),
        'status-info': withVar('status-info'),
        'status-standby': withVar('status-standby'),
      },
      fontSize: {
        caption: ['12px', { lineHeight: '1.4', letterSpacing: '0.01em' }],
        body: ['14px', { lineHeight: '1.55', letterSpacing: '0em' }],
        'body-lg': ['17px', { lineHeight: '1.5', letterSpacing: '0em' }],
        h3: ['20px', { lineHeight: '1.3', letterSpacing: '-0.005em', fontWeight: '600' }],
        h2: ['24px', { lineHeight: '1.25', letterSpacing: '-0.01em', fontWeight: '600' }],
        h1: ['29px', { lineHeight: '1.15', letterSpacing: '-0.015em', fontWeight: '700' }],
        display: ['42px', { lineHeight: '1.05', letterSpacing: '-0.02em', fontWeight: '700' }],
      },
      fontFamily: {
        display: ['Outfit', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        body: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        zh: ['chironHeiHK', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'sans-serif'],
      },
      spacing: {
        micro: '4px',
        tight: '8px',
        snug: '12px',
        base: '16px',
        comfortable: '24px',
        'stack-sm': '24px',
        'stack-md': '32px',
        loose: '32px',
        section: '64px',
        'stack-lg': '64px',
        'section-y': '96px',
        macro: '128px',
      },
      borderRadius: {
        sm: '8px',
        md: '8px',
        lg: '12px',
        card: '12px',
        xl: '16px',
        button: '8px',
        input: '8px',
      },
      boxShadow: {
        'level-1': '0 1px 3px rgba(0,0,0,0.06), 0 4px 12px rgba(0,0,0,0.04)',
        'level-2': '0 4px 12px rgba(0,0,0,0.10), 0 8px 24px rgba(0,0,0,0.06)',
        'level-3': '0 10px 30px rgba(0,0,0,0.15)',
        'level-4': '0 20px 40px rgba(0,0,0,0.2)',
        card: '0 1px 3px rgba(0,0,0,0.06), 0 4px 12px rgba(0,0,0,0.04)',
      },
    },
  },
  plugins: [],
};
