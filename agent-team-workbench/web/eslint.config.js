import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';

// 最小 lint 基线：TS 推荐规则 + react-hooks（依赖数组正确性）。
// 故意不开 stylistic / formatting 类规则，避免与现有代码风格冲突。
export default tseslint.config(
  { ignores: ['dist', 'node_modules'] },
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // set-state-in-effect 是 react-hooks v7 新增的激进规则，命中存量
      // 「prop → 本地草稿态」同步模式（agents/models/settings/block-modal）。
      // 正确修法是组件重构（key 重挂载 / render 期派生），超出 lint 基线范围，
      // 先关闭并在报告中记为遗留项。
      'react-hooks/set-state-in-effect': 'off',
    },
  },
);
