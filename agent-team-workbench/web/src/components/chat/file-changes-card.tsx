import { FileCode2, RotateCcw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getRunChangeDiff, listRunChanges, revertRunChanges } from '../../api/endpoints';
import type { RunChange, RunChanges } from '../../api/types';
import { ApiError } from '../../api/client';
import { Drawer } from '../drawer';
import { Modal } from '../modal';
import { DiffCard } from './diff-card';
import css from './file-changes-card.module.css';
import { useRunsStore } from '../../stores/runs.store';
import { captureScope, isCurrent, type Scope } from '../../stores/scope';

/** 同一 run 的旧 changes 响应不能覆盖更晚的 SSE 回滚补拉。 */
export interface RunChangesRequestFence {
  begin: () => number;
  accepts: (requestId: number, scope: Scope) => boolean;
}

export function createRunChangesRequestFence(): RunChangesRequestFence {
  let currentRequest = 0;
  return {
    begin: () => {
      currentRequest += 1;
      return currentRequest;
    },
    accepts: (requestId, scope) => requestId === currentRequest && isCurrent(scope),
  };
}

export function FileChangesCard({ runId }: { runId: string }) {
  const changesRevision = useRunsStore((state) => state.changesRevision[runId] ?? 0);
  const [data, setData] = useState<RunChanges | null>(null);
  const [error, setError] = useState('');
  const [review, setReview] = useState(false);
  const [revertOpen, setRevertOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const requestFenceRef = useRef<RunChangesRequestFence | null>(null);
  requestFenceRef.current ??= createRunChangesRequestFence();

  const refresh = useCallback(async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const requestId = requestFenceRef.current!.begin();
    try {
      const value = await listRunChanges(runId);
      if (!requestFenceRef.current!.accepts(requestId, scope)) return;
      setData(value);
      setError('');
    } catch (value) {
      if (!requestFenceRef.current!.accepts(requestId, scope)) return;
      if (value instanceof ApiError && value.status === 404) {
        setData(null);
        setError('');
        return;
      }
      setError(value instanceof Error ? value.message : '无法读取文件变更');
    }
  }, [runId]);

  // 回滚命令可能来自另一个窗口；SSE/replay 经 runs store 推进 revision 后，
  // 重新读取权威 changes 读模型，不能继续展示本地旧的可撤销状态。
  useEffect(() => {
    if (changesRevision > 0) setRevertOpen(false);
    void refresh();
  }, [changesRevision, refresh]);

  if (!data) {
    return error ? <div className={css.error} role="alert">{error}</div> : null;
  }
  if (data.state === 'unavailable' || data.files.length === 0) return null;

  const reverted = data.state === 'reverted';
  const revertReason = reverted ? '本轮文件变更已经撤销' : data.reason || '当前文件已被继续修改，无法安全撤销';

  return (
    <>
      <FileChangesView
        data={data}
        busy={busy}
        onReview={() => setReview(true)}
        onRequestRevert={() => setRevertOpen(true)}
      />
      <Modal open={revertOpen} onClose={() => !busy && setRevertOpen(false)} title="撤销本轮文件变更">
        <p className="mb-4 text-body text-text-secondary">
          系统会先确认这些文件仍与本轮结束时一致，再整体恢复到本轮写入前；任何文件被继续修改都不会写回。
        </p>
        <div className="flex justify-end gap-2">
          <button type="button" className={css.button} onClick={() => setRevertOpen(false)}>取消</button>
          <button
            type="button"
            className={`${css.button} ${css.danger}`}
            disabled={busy}
            onClick={async () => {
              const scope = captureScope();
              if (!scope.workspaceId) return;
              const requestId = requestFenceRef.current!.begin();
              setBusy(true);
              try {
                const value = await revertRunChanges(runId, crypto.randomUUID());
                if (!requestFenceRef.current!.accepts(requestId, scope)) return;
                setData(value);
                setError('');
                setRevertOpen(false);
              } catch (value) {
                if (!requestFenceRef.current!.accepts(requestId, scope)) return;
                setError(value instanceof Error ? value.message : '撤销失败');
              } finally {
                // SSE 可能先于 HTTP 响应送达并触发较新的重拉；busy 属于这次
                // 命令本身，不能因读请求换代而永久卡住确认按钮。
                if (isCurrent(scope)) setBusy(false);
              }
            }}
          >
            {busy ? '正在撤销…' : '确认撤销'}
          </button>
        </div>
      </Modal>
      <FileChangesDrawer runId={runId} items={data.files} open={review} onClose={() => setReview(false)} />
      {error && <div className={css.error} role="alert">{error}</div>}
      {!data.can_revert && !reverted && <span className="sr-only">{revertReason}</span>}
    </>
  );
}

export function FileChangesView({
  data,
  busy = false,
  onReview,
  onRequestRevert,
}: {
  data: RunChanges;
  busy?: boolean;
  onReview?: () => void;
  onRequestRevert?: () => void;
}) {
  const reverted = data.state === 'reverted';
  const canRevert = data.can_revert && !reverted && !busy;
  const reason = reverted ? '本轮文件变更已经撤销' : data.reason || '当前变更无法安全撤销';
  return (
    <section className={css.card} aria-label="本轮文件变更">
      <div className={css.head}>
        <span className={css.icon}><FileCode2 size={19} aria-hidden /></span>
        <div className={css.heading}>
          <div className={css.titleLine}>
            <span className={css.title}>已编辑 {data.file_count} 个文件</span>
            <span className={css.totalStats} aria-label={`新增 ${data.additions} 行，删除 ${data.deletions} 行`}>
              <span className={css.add}>+{data.additions}</span>
              <span className={css.del}>−{data.deletions}</span>
            </span>
            {reverted && <span className={css.reverted}>已撤销</span>}
          </div>
          <div className={css.sub}>查看更改 ↗</div>
        </div>
        <div className={css.actions}>
          <button
            className={`${css.button} ${css.danger}`}
            type="button"
            disabled={!canRevert}
            title={!canRevert ? reason : '撤销本轮文件变更'}
            onClick={onRequestRevert}
          >
            <RotateCcw size={14} aria-hidden />撤销
          </button>
          <button className={css.button} type="button" onClick={onReview}>审核</button>
        </div>
      </div>
      <ChangeList items={data.files} />
    </section>
  );
}

export function ChangeList({ items }: { items: RunChange[] }) {
  const [all, setAll] = useState(false);
  const visible = all ? items : items.slice(0, 3);
  return (
    <div className={css.list}>
      {visible.map((item) => (
        <div className={css.row} key={item.path}>
          <span className={css.path} title={item.path}>{item.path}</span>
          <ChangeStats item={item} />
        </div>
      ))}
      {items.length > 3 && (
        <button className={css.more} type="button" aria-expanded={all} onClick={() => setAll((value) => !value)}>
          {all ? '收起文件' : `再显示 ${items.length - 3} 个文件`}
        </button>
      )}
    </div>
  );
}

function ChangeStats({ item }: { item: RunChange }) {
  return (
    <span className={css.stats} aria-label={`${item.path}，新增 ${item.additions} 行，删除 ${item.deletions} 行`}>
      <span className={css.add}>+{item.additions}</span>
      <span className={css.del}>−{item.deletions}</span>
    </span>
  );
}

function FileChangesDrawer({ runId, items, open, onClose }: {
  runId: string;
  items: RunChange[];
  open: boolean;
  onClose: () => void;
}) {
  const [selectedPath, setSelectedPath] = useState(items[0]?.path ?? '');
  const [diff, setDiff] = useState('');
  const [loading, setLoading] = useState(false);
  const selected = useMemo(() => items.find((item) => item.path === selectedPath) ?? items[0], [items, selectedPath]);

  useEffect(() => { setSelectedPath(items[0]?.path ?? ''); }, [items]);
  useEffect(() => {
    if (!open || !selected) return;
    let active = true;
    setLoading(true);
    void getRunChangeDiff(runId, selected.path)
      .then((value) => { if (active) setDiff(value.diff); })
      .catch(() => { if (active) setDiff(''); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [open, runId, selected]);

  return (
    <Drawer open={open} onClose={onClose} title="审核文件变更" width={760}>
      <div className={css.drawer}>
        <aside className={css.fileRail} aria-label="变更文件">
          <div className={css.fileCount}>{items.length} 个文件</div>
          {items.map((item) => (
            <button
              type="button"
              key={item.path}
              className={`${css.select} ${selected?.path === item.path ? css.selected : ''}`}
              aria-pressed={selected?.path === item.path}
              onClick={() => setSelectedPath(item.path)}
            >
              <span className={css.selectPath}>{item.path}</span>
              <ChangeStats item={item} />
            </button>
          ))}
        </aside>
        <div className={css.diff} aria-live="polite">
          {loading ? <div className={css.empty}>正在加载 Diff…</div> : diff
            ? <DiffCard text={diff} />
            : <div className={css.empty}>该文件暂无可预览的文本 Diff</div>}
        </div>
      </div>
    </Drawer>
  );
}
