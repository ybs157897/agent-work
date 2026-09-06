import { apiFetch } from './client';

/** 任务发布前的澄清消息；此记录不会创建 WorkItem 或 Run。 */
export interface TaskIntakeMessage {
  role: 'user' | 'assistant';
  content: string;
  /** 服务端生成的需求澄清题；旧记录没有此字段时仍按纯正文显示。 */
  questions?: TaskIntakeQuestion[];
}

export interface TaskIntakeQuestion {
  id: string;
  title: string;
  options: string[];
  multiple: boolean;
}

/** 分析服务产出的可编辑任务草案。 */
export interface TaskIntakeDraft {
  title: string;
  description: string;
  acceptance_criteria: string[];
}

export interface AnalyzeTaskIntakeInput {
  messages: TaskIntakeMessage[];
  draft?: TaskIntakeDraft;
  /** 可选模型注册表 ref；省略时由服务端解析任务默认模型。 */
  model_ref?: string;
}

export interface AnalyzeTaskIntakeResponse {
  reply: string;
  draft?: TaskIntakeDraft;
  questions?: TaskIntakeQuestion[];
}

/**
 * Stateless 任务澄清请求。分析回合和最终 WorkItem 发布是两条独立的控制线；
 * 这个函数只负责取得助手回复，不具有发布任务的权限。
 */
export const analyzeTaskIntake = (workspaceId: string, input: AnalyzeTaskIntakeInput) =>
  apiFetch<AnalyzeTaskIntakeResponse>(`/workspaces/${workspaceId}/task-intake/analyze`, {
    method: 'POST',
    body: input,
  });
