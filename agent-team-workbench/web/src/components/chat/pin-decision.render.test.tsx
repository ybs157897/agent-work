import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { PinDecisionAction } from './pin-decision-action';

describe('PinDecisionAction', () => {
  it('空闲态可点：有目标任务与非空原文', () => {
    const html = renderToStaticMarkup(
      <PinDecisionAction quote="原文" workItemId="wi_1" sourceRunId="run_1" idempotencyKey="decision:wi_1:k" />,
    );
    expect(html).toContain('aria-label="钉为决策"');
    expect(html).toContain('钉为决策：原文记入任务台账');
    expect(html).not.toContain('disabled=""');
  });

  it('无目标任务（未在会话中）禁用并说明原因', () => {
    const html = renderToStaticMarkup(<PinDecisionAction quote="原文" />);
    expect(html).toContain('disabled=""');
    expect(html).toContain('未在会话中，无法钉为决策');
  });

  it('空原文禁用（服务端 quote 必填 trim 非空）', () => {
    const html = renderToStaticMarkup(<PinDecisionAction quote="   " workItemId="wi_1" />);
    expect(html).toContain('disabled=""');
  });
});
