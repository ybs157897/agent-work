// TerminalBlock：shell 命令的终端卡——prompt 区（状态点 + cwd 标签 + 逐行命令）、
// ANSI 上色输出、落定状态 Pill 与原始输出复制。移植自 DSH ui-primitives 的
// TerminalBlock：中文文案内建（不注入 labels）、无 home 收叠。输出永不软换行：
// 列对齐的输出（ls、表格、框线）靠横向滚动保住对齐。

import { useMemo, useState, type ReactNode } from 'react';
import { cx } from './cx';
import { parseAnsiLines, type AnsiLine } from './ansi';
import { headTailCap } from './head-tail-cap';
import { useCopyFeedback } from './use-copy-feedback';
import { StateDot, type StateDotState } from './StateDot';
import css from './TerminalBlock.module.css';

/** 输出超过该行数后中段折叠（头 ceil(n/2) 行 + 尾行），对齐 TUI 工具输出预算。 */
export const DEFAULT_TERMINAL_MAX_LINES = 16;

export interface TerminalBlockProps {
  /** 命令文本，prompt 区按 \n 逐行渲染；尾换行只是终止符不算空命令。 */
  command: string;
  /** 工作目录标签（取路径最后一段）；缺省显 `$`。 */
  cwd?: string | undefined;
  /** 命令输出，可含 ANSI 转义序列。 */
  output?: string | undefined;
  /** 落定退出码；非 0 显状态 Pill。 */
  exitCode?: number | undefined;
  /** 落定终止信号名；任何值都显 Pill，且优先于退出码。 */
  signal?: string | undefined;
  /** 命令运行中：只渲染 prompt 区。 */
  running?: boolean | undefined;
  /** 输出区行数上限，超出折叠中段（默认 16）。 */
  maxLines?: number | undefined;
  /** 追加到卡根的类名（调用方负责定位，本组件只画形）。 */
  className?: string | undefined;
}

/** cwd 的 prompt 标签：路径最后一段（两种分隔符都认、忽略尾分隔符），无段回落原串。 */
function promptLabel(cwd: string): string {
  const trimmed = cwd.replace(/[/\\]+$/, '');
  const segment = trimmed.split(/[/\\]/).pop();
  return segment === undefined || segment === '' ? cwd : segment;
}

/** 多行命令按 \n 拆成逐行 prompt；尾换行是终止符，不产生空命令行。 */
export function commandLines(command: string): string[] {
  const body = command.endsWith('\n') ? command.slice(0, -1) : command;
  return body.split('\n');
}

/** 落定状态 Pill 文案：信号优先于退出码；干净落定（exit 0 且无信号）无 Pill。 */
export function terminalStatusText(
  exitCode: number | undefined,
  signal: string | undefined,
): string | undefined {
  if (signal !== undefined) return `信号 ${signal}`;
  if (exitCode !== undefined && exitCode !== 0) return `退出码 ${exitCode}`;
  return undefined;
}

/** 空输出判定在解析后的行上做：全行全 span trim 后皆空才算空（纯转义/控制字节输出
 *  trim 原文仍非空但解析后无可见内容）；占位符与复制按钮的显隐都依它。 */
export function outputIsEmpty(lines: AnsiLine[]): boolean {
  return lines.every((line) => line.every((span) => span.text.trim() === ''));
}

/** 命令运行态：running→蓝 ongoing；信号/非零退出→红 error；干净落定→绿 done。
 * label 是状态点（aria 隐藏）的读屏文本。 */
function runState(
  running: boolean,
  exitCode: number | undefined,
  signal: string | undefined,
): { state: StateDotState; label: string } {
  if (running) return { state: 'ongoing', label: '运行中' };
  if (terminalStatusText(exitCode, signal) !== undefined) return { state: 'error', label: '失败' };
  return { state: 'done', label: '已完成' };
}

/** 渲染一行解析结果：无 style 的 span 渲染裸文本（不带 span 包装），上色行才包 span。 */
function renderLine(line: AnsiLine): ReactNode {
  return line.map((span, index) =>
    span.style === undefined ? span.text : <span key={index} style={span.style}>{span.text}</span>,
  );
}

export function TerminalBlock({
  command,
  cwd,
  output,
  exitCode,
  signal,
  running = false,
  maxLines = DEFAULT_TERMINAL_MAX_LINES,
  className,
}: TerminalBlockProps) {
  const text = output ?? '';
  // 命令输出以换行收尾：该终止符不是要画的空行，也不占行数上限。判定在解析后的
  // 行上做（末行 reset 转义会让原文不以 \n 结尾却解析出空末行）；真正的双换行空行
  // 在终止符之前，保留。复制内容仍是原始 output 文本。
  const lines = useMemo(() => {
    const parsed = parseAnsiLines(text);
    const last = parsed[parsed.length - 1];
    const terminated = parsed.length > 1 && last !== undefined && last.every((span) => span.text === '');
    return terminated ? parsed.slice(0, -1) : parsed;
  }, [text]);
  const [expanded, setExpanded] = useState(false);
  // 复制原始输出：prompt 行与状态 Pill 是用户没跑过的装饰，不进剪贴板。
  const { copied, onCopy } = useCopyFeedback(text);

  const status = terminalStatusText(exitCode, signal);
  const state = runState(running, exitCode, signal);
  const prompts = useMemo(() => commandLines(command), [command]);
  const empty = outputIsEmpty(lines);
  const { hidden, capped, headLines, tailLines } = headTailCap(lines.length, maxLines, expanded);

  return (
    <div className={cx(css.block, className)} data-running={running ? '' : undefined}>
      <div className={css.header}>
        <div className={css.prompt}>
          <span className={css.runStateLabel}>{state.label}</span>
          {prompts.map((line, index) => (
            <div key={index} className={css.promptLine}>
              {/* 状态点只画在第一行：整卡只有一个（本次调用的）落定状态。 */}
              {index === 0 && <StateDot state={state.state} className={css.runState} />}
              {/* cwd 标注的是这次调用，只随第一行；后续行保持裸 `$` 对齐 prompt 形态。 */}
              <span className={css.cwd}>{index > 0 || cwd === undefined ? '$' : promptLabel(cwd)}</span>
              <span className={css.command}>{line}</span>
            </div>
          ))}
        </div>
        {status !== undefined && <span className={css.pill}>{status}</span>}
        {!running && !empty && (
          <button type="button" className={css.copyButton} onClick={onCopy}>
            {copied ? '复制成功' : '复制'}
          </button>
        )}
      </div>
      {!running &&
        (empty ? (
          <div className={css.empty}>无输出</div>
        ) : (
          <div className={css.output}>
            {(capped ? lines.slice(0, headLines) : lines).map((line, index) => (
              <div key={index} className={css.line}>{renderLine(line)}</div>
            ))}
            {hidden > 0 && (
              <button
                type="button"
                className={css.expand}
                aria-expanded={expanded}
                aria-label={expanded ? '收起输出' : `展开其余 ${hidden} 行输出`}
                onClick={() => setExpanded((v) => !v)}
              >
                {expanded ? '收起' : `… 其余 ${hidden} 行`}
              </button>
            )}
            {capped &&
              lines.slice(lines.length - tailLines).map((line, index) => (
                <div key={`tail-${index}`} className={css.line}>{renderLine(line)}</div>
              ))}
          </div>
        ))}
    </div>
  );
}
