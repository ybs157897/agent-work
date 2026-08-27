import {
  AlertCircle,
  Check,
  ChevronDown,
  CircleDashed,
  CircleX,
  ExternalLink,
  FileCode2,
  Info,
  ListChecks,
  LoaderCircle,
  Route,
  ShieldAlert,
  ShieldCheck,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react';
import { useId } from 'react';
import type { ReviewSeverity, ReviewSummaryBlock as ReviewSummaryData } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

type StatusPresentation = { label: string; className: string; icon: LucideIcon };

const VERDICTS: Record<ReviewSummaryData['verdict'], StatusPresentation> = {
  passed: { label: '评审通过', className: 'chat-review-tone-success', icon: ShieldCheck },
  passed_with_warnings: { label: '通过，但有注意事项', className: 'chat-review-tone-warning', icon: TriangleAlert },
  changes_requested: { label: '需要修改', className: 'chat-review-tone-error', icon: ShieldAlert },
  blocked: { label: '评审受阻', className: 'chat-review-tone-error', icon: CircleX },
  inconclusive: { label: '评审未完成', className: 'chat-review-tone-neutral', icon: AlertCircle },
};

const SEVERITIES: Record<ReviewSeverity, StatusPresentation> = {
  critical: { label: '严重', className: 'chat-review-tone-error', icon: CircleX },
  high: { label: '高', className: 'chat-review-tone-error', icon: ShieldAlert },
  medium: { label: '中', className: 'chat-review-tone-warning', icon: TriangleAlert },
  low: { label: '低', className: 'chat-review-tone-info', icon: Info },
  info: { label: '信息', className: 'chat-review-tone-neutral', icon: CircleDashed },
};

const CHECKS: Record<ReviewSummaryData['checks'][number]['status'], StatusPresentation> = {
  passed: { label: '通过', className: 'chat-review-tone-success', icon: Check },
  failed: { label: '失败', className: 'chat-review-tone-error', icon: CircleX },
  warning: { label: '注意', className: 'chat-review-tone-warning', icon: TriangleAlert },
  skipped: { label: '未执行', className: 'chat-review-tone-neutral', icon: CircleDashed },
  running: { label: '进行中', className: 'chat-review-tone-info', icon: LoaderCircle },
};

export function ReviewSummaryBlock({ block }: { block: ReviewSummaryData }) {
  const verdict = VERDICTS[block.verdict];
  const VerdictIcon = verdict.icon;
  const titleId = useId();
  const stats = block.stats ? [
    block.stats.files !== undefined ? { label: '评审文件', value: block.stats.files } : null,
    block.stats.findings !== undefined ? { label: '有效发现', value: block.stats.findings } : null,
    block.stats.passed !== undefined ? { label: '检查通过', value: block.stats.passed } : null,
  ].filter((item): item is { label: string; value: number } => item !== null) : [];

  return (
    <ContentBlockShell block={block} icon={ListChecks}>
      <div className="chat-review-summary">
        <div
          className={`chat-review-verdict ${verdict.className}`}
          role={block.verdict === 'blocked' || block.verdict === 'changes_requested' ? 'alert' : 'status'}
          aria-label={`${verdict.label}：${block.summary}`}
        >
          <span className="chat-review-verdict-icon" aria-hidden><VerdictIcon /></span>
          <div className="chat-review-verdict-copy">
            <p>{verdict.label}</p>
            <span>{block.summary}</span>
          </div>
        </div>

        {stats.length > 0 && (
          <dl className="chat-review-stats" aria-label="评审统计">
            {stats.map((item) => (
              <div key={item.label}>
                <dt>{item.label}</dt>
                <dd>{item.value.toLocaleString()}</dd>
              </div>
            ))}
          </dl>
        )}

        {block.findings.length > 0 && (
          <section className="chat-review-section" aria-labelledby={`${titleId}-findings`}>
            <header className="chat-review-section-head">
              <h4 id={`${titleId}-findings`}>主要发现</h4>
              <span>{block.findings.length} 项</span>
            </header>
            <ol className="chat-review-findings">
              {block.findings.map((finding, index) => {
                const severity = SEVERITIES[finding.severity];
                const SeverityIcon = severity.icon;
                const location = finding.file
                  ? `${finding.file}${finding.line !== undefined ? `:${finding.line}` : ''}`
                  : finding.line !== undefined ? `第 ${finding.line} 行` : undefined;
                return (
                  <li key={`${finding.severity}-${finding.title}-${index}`}>
                    <details
                      className={`chat-review-finding ${severity.className}`}
                      open={index === 0 && (finding.severity === 'critical' || finding.severity === 'high')}
                    >
                      <summary>
                        <span className="chat-review-severity" aria-label={`严重度：${severity.label}`}>
                          <SeverityIcon aria-hidden />
                          {severity.label}
                        </span>
                        <span className="chat-review-finding-title">{finding.title}</span>
                        {location && <code className="chat-review-location-preview">{location}</code>}
                        <ChevronDown className="chat-review-finding-chevron" aria-hidden />
                      </summary>
                      <div className="chat-review-finding-body">
                        {finding.detail && <p>{finding.detail}</p>}
                        {location && (
                          finding.url ? (
                            <a className="chat-review-location" href={finding.url} target="_blank" rel="noreferrer noopener">
                              <FileCode2 aria-hidden />
                              <code>{location}</code>
                              <ExternalLink aria-hidden />
                            </a>
                          ) : (
                            <span className="chat-review-location">
                              <FileCode2 aria-hidden />
                              <code>{location}</code>
                            </span>
                          )
                        )}
                        {finding.evidence && <pre className="chat-review-evidence">{finding.evidence}</pre>}
                        {finding.suggestion && (
                          <p className="chat-review-suggestion"><strong>建议</strong>{finding.suggestion}</p>
                        )}
                        {!location && finding.url && (
                          <a className="chat-review-source-link" href={finding.url} target="_blank" rel="noreferrer noopener">
                            打开证据链接 <ExternalLink aria-hidden />
                          </a>
                        )}
                      </div>
                    </details>
                  </li>
                );
              })}
            </ol>
          </section>
        )}

        {block.checks.length > 0 && (
          <section className="chat-review-section" aria-labelledby={`${titleId}-checks`}>
            <header className="chat-review-section-head">
              <h4 id={`${titleId}-checks`}>验证结果</h4>
              <span>{block.checks.filter((check) => check.status === 'passed').length}/{block.checks.length} 通过</span>
            </header>
            <ul className="chat-review-checks" aria-live="polite">
              {block.checks.map((check, index) => {
                const status = CHECKS[check.status];
                const CheckIcon = status.icon;
                return (
                  <li key={`${check.label}-${index}`}>
                    <span className={`chat-review-check-icon ${status.className}`} aria-hidden>
                      <CheckIcon className={check.status === 'running' ? 'chat-review-spin' : undefined} />
                    </span>
                    <span className="chat-review-check-copy">
                      <strong>{check.label}</strong>
                      {check.detail && <span>{check.detail}</span>}
                      {check.command && <code>{check.command}</code>}
                    </span>
                    <span className={`chat-review-check-status ${status.className}`}>{status.label}</span>
                  </li>
                );
              })}
            </ul>
          </section>
        )}

        {block.nextSteps.length > 0 && (
          <section className="chat-review-section chat-review-next" aria-labelledby={`${titleId}-next`}>
            <header className="chat-review-section-head">
              <h4 id={`${titleId}-next`}><Route aria-hidden />下一步</h4>
            </header>
            <ol>
              {block.nextSteps.map((step, index) => (
                <li key={`${step.label}-${index}`}>
                  <span aria-hidden>{index + 1}</span>
                  <p><strong>{step.label}</strong>{step.detail && <small>{step.detail}</small>}</p>
                </li>
              ))}
            </ol>
          </section>
        )}
      </div>
    </ContentBlockShell>
  );
}
