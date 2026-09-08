export type DirEntry = {
  name: string
  type: 'file' | 'dir'
  size?: number
  modified_at?: string
}

export type TreeResponse = {
  path: string
  truncated?: boolean
  entries: DirEntry[]
}

export type Workspace = {
  id: string
  status: string
  root_display?: string
  lsp_enabled?: boolean
  lsp_status?: string
  read_only?: boolean
}

export type SearchHit = {
  path: string
  line: number
  column: number
  preview: string
}

export type SearchResponse = {
  query: string
  hits: SearchHit[]
  truncated?: boolean
}

export type FilesResponse = {
  query: string
  paths: string[]
  truncated?: boolean
}


export type ProjectJDK = {
  home?: string
  version?: string
  vendor?: string
}

export type ProjectModule = {
  path: string
  artifact_id?: string
  group_id?: string
  version?: string
  packaging?: string
  name?: string
}

export type ProjectDependency = {
  group_id: string
  artifact_id: string
  version?: string
  scope?: string
  coord: string
}

export type ProjectInfo = {
  build: 'maven' | 'gradle' | 'none'
  jdk: ProjectJDK
  language_level?: string
  root_module?: ProjectModule
  modules?: ProjectModule[]
  dependencies?: ProjectDependency[]
  dependencies_truncated?: boolean
}

export class GatewayError extends Error {
	readonly status: number

	constructor(
		message: string,
		status: number,
	) {
		super(message)
		this.name = 'GatewayError'
		this.status = status
  }
}

export type StatResult = {
  path: string
  type: 'file' | 'dir'
  size?: number
  modified_at?: string
}

export class GatewayClient {
	readonly baseUrl: string
	readonly token: string

	constructor(
		baseUrl: string,
		token: string,
	) {
		this.baseUrl = baseUrl
		this.token = token
	}

	private async request(path: string, init: RequestInit = {}): Promise<Response> {
		const headers = new Headers(init.headers)
		if (this.token) headers.set('Authorization', `Bearer ${this.token}`)
    if (init.body && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }
    const res = await fetch(`${this.baseUrl.replace(/\/$/, '')}${path}`, {
      ...init,
      headers,
    })
    return res
  }

  async createWorkspace(root: string): Promise<Workspace> {
    const res = await this.request('/api/v1/workspaces', {
      method: 'POST',
      body: JSON.stringify({ root }),
    })
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<Workspace>
  }

  async tree(workspaceId: string, path = '', depth = 1): Promise<TreeResponse> {
    const q = new URLSearchParams({ path, depth: String(depth) })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/tree?${q}`)
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<TreeResponse>
  }

  async stat(workspaceId: string, path: string): Promise<StatResult> {
    const q = new URLSearchParams({ path })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/stat?${q}`)
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<StatResult>
  }

  async readFile(workspaceId: string, path: string): Promise<string> {
    const q = new URLSearchParams({ path })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/file?${q}`)
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.text()
  }

  async search(
    workspaceId: string,
    query: string,
    limit = 200,
  ): Promise<SearchResponse> {
    const q = new URLSearchParams({ q: query, limit: String(limit) })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/search?${q}`)
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<SearchResponse>
  }

  async files(
    workspaceId: string,
    query: string,
    limit = 60,
    signal?: AbortSignal,
  ): Promise<FilesResponse> {
    const q = new URLSearchParams({ q: query, limit: String(limit) })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/files?${q}`, { signal })
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<FilesResponse>
  }


  async project(workspaceId: string): Promise<ProjectInfo> {
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/project`)
    if (!res.ok) {
      throw await this.toError(res)
    }
    return res.json() as Promise<ProjectInfo>
  }


  async writeFile(workspaceId: string, path: string, content: string): Promise<void> {
    const q = new URLSearchParams({ path })
    const res = await this.request(`/api/v1/workspaces/${workspaceId}/fs/file?${q}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      body: content,
    })
    if (!res.ok) {
      throw await this.toError(res)
    }
  }

  async deleteWorkspace(workspaceId: string): Promise<void> {
    const res = await this.request(`/api/v1/workspaces/${workspaceId}`, { method: 'DELETE' })
    if (!res.ok) {
      throw await this.toError(res)
    }
  }

  private async toError(res: Response): Promise<GatewayError> {
    let msg = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      /* ignore */
    }
    return new GatewayError(msg, res.status)
  }
}
