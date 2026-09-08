import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { expect, it } from 'vitest';
import { SendErrorNotice } from './send-error-notice';

it('未创建 Run 的发送失败有常驻原因、原文保留说明和准确配置入口', () => {
  const html = renderToStaticMarkup(<MemoryRouter><SendErrorNotice message="runtime binding unavailable" agentId="nova" /></MemoryRouter>);
  expect(html).toContain('role="alert"');
  expect(html).toContain('消息暂未发送，原文已保留');
  expect(html).toContain('href="/agents?agent=nova"');
  expect(html).toContain('查看失败原因');
  expect(html).toContain('runtime binding unavailable');
});
