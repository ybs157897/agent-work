import { Check, Loader2, Pin } from 'lucide-react';
import { useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { createDecision } from '../../api/endpoints';
import { toast } from '../../stores/toast.store';
import { canPin, decisionPayload, type PinState } from '../../utils/decision-pin';

/**
 * 「钉为决策」消息小动作（S2）：把用户消息原文钉进任务台账。
 * quote = 消息原文，source_run_id = 该消息所属 run；在途与已钉过防重复点击
 * （本地态 + 同 tick ref 双闸），失败回 idle 可重试——同键重试由服务端幂等去重。
 */
export function PinDecisionAction({
  quote,
  workItemId,
  sourceRunId,
  idempotencyKey,
}: {
  quote: string;
  /** 目标任务（当前会话）；缺省时禁用。 */
  workItemId?: string;
  /** 来源轮次；缺省时钉为无来源。 */
  sourceRunId?: string;
  /** 幂等键：同一消息重复点击查回既有台账行（createRun client_key 惯例）。 */
  idempotencyKey?: string;
}) {
  const [state, setState] = useState<PinState>('idle');
  // 同一渲染帧内的连点先于 state 更新落地，用 ref 同步兜闸。
  const busy = useRef(false);

  const onPin = () => {
    if (busy.current || !workItemId || !canPin(state, quote, workItemId)) return;
    busy.current = true;
    setState('pinning');
    void (async () => {
      try {
        await createDecision(workItemId, decisionPayload(quote, sourceRunId), idempotencyKey);
        toast.success('已钉为决策');
        setState('pinned');
      } catch (err) {
        toast.error(err instanceof ApiError ? err.message : '钉为决策失败');
        setState('idle');
      } finally {
        busy.current = false;
      }
    })();
  };

  const label = state === 'pinned' ? '已钉为决策' : state === 'pinning' ? '钉为决策中' : '钉为决策';
  const title =
    state === 'pinned'
      ? '已钉进任务台账'
      : state === 'pinning'
        ? '正在钉为决策'
        : workItemId
          ? '钉为决策：原文记入任务台账'
          : '未在会话中，无法钉为决策';

  return (
    <button
      type="button"
      aria-label={label}
      title={title}
      disabled={!canPin(state, quote, workItemId)}
      onClick={onPin}
      className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
    >
      {state === 'pinned' ? (
        <Check className="h-3.5 w-3.5 text-status-success" />
      ) : state === 'pinning' ? (
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
      ) : (
        <Pin className="h-3.5 w-3.5" />
      )}
    </button>
  );
}
