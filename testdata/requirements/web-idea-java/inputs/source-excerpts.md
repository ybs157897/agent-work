# 真实代码证据摘录

Source-ID: SRC-CODE-01
Kind: repo_fact
Authority: observed_source_snapshot

以下摘录在本轮从两个当前代码表面读取；独立项目包含未提交修改，不能仅凭HEAD重建。每个来源保留完整文件SHA-256与行号。已截短的范围会明确说明。

## CODE-01 · standalone-web-idea · apps/gateway/internal/api/server.go
File-SHA256: 468b0d8ddd5e137f0f4075e83bc44eada2c845b22dea8f258712c1aa24cd5bf7
Original-Ranges: 40-51,76-98
Summary: Gateway routes and standalone workspace creation; includes PUT file and LSP endpoints.

```text
40: 	api := http.NewServeMux()
41: 	api.HandleFunc("POST /api/v1/workspaces", s.handleCreateWorkspace)
42: 	api.HandleFunc("GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
43: 	api.HandleFunc("DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
44: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/tree", s.handleTree)
45: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/file", s.handleFile)
46: 	api.HandleFunc("PUT /api/v1/workspaces/{id}/fs/file", s.handlePutFile)
47: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/stat", s.handleStat)
48: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/search", s.handleSearch)
49: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/files", s.handleFiles)
50: 	api.HandleFunc("GET /api/v1/workspaces/{id}/project", s.handleProject)
51: 	api.HandleFunc("GET /api/v1/workspaces/{id}/lsp", s.handleLSP)
```

```text
76: func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
77: 	var req createReq
78: 	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
79: 	if err := dec.Decode(&req); err != nil {
80: 		writeErr(w, http.StatusBadRequest, "invalid json")
81: 		return
82: 	}
83: 	ws, err := s.store.Create(req.Root, s.cfg.MaxFileBytes)
84: 	if err != nil {
85: 		switch {
86: 		case errors.Is(err, workspace.ErrInvalidRoot), errors.Is(err, workspace.ErrRootNotDir):
87: 			writeErr(w, http.StatusBadRequest, err.Error())
88: 		default:
89: 			if osIsNotExist(err) {
90: 				writeErr(w, http.StatusBadRequest, "root not found")
91: 				return
92: 			}
93: 			writeErr(w, http.StatusBadRequest, err.Error())
94: 		}
95: 		return
96: 	}
97: 	writeJSON(w, http.StatusCreated, s.workspaceJSON(ws))
98: }
```

## CODE-02 · standalone-web-idea · apps/gateway/internal/lspproxy/proxy.go
File-SHA256: 847578ab00fd96b285b9f759b52b4393a18248ed0ade49422e93223013730e2c
Original-Ranges: 20-95
Summary: WebSocket to jdtls stdio bridge without the embedded read-only method filter.

```text
20: 	if err != nil {
21: 		log.Printf("lspproxy upgrade: %v", err)
22: 		return
23: 	}
24: 	defer conn.Close()
25: 	conn.SetReadLimit(MaxFrameBytes)
26:
27: 	var writeMu sync.Mutex
28: 	errCh := make(chan error, 2)
29: 	handlerDone := make(chan struct{})
30: 	defer close(handlerDone)
31:
32: 	// A workspace DELETE stops the session and closes Done after the process
33: 	// exits. Closing the WebSocket here also unblocks the client reader when a
34: 	// browser connection is still open during that shutdown.
35: 	go func() {
36: 		select {
37: 		case <-sess.Done():
38: 			_ = conn.Close()
39: 		case <-r.Context().Done():
40: 			_ = conn.Close()
41: 		case <-handlerDone:
[范围中间 46 行未复制；请按原文件与摘要复核]
88: 				errCh <- err
89: 				return
90: 			}
91: 		}
92: 	}()
93:
94: 	<-errCh
95: }
```

## CODE-03 · standalone-web-idea · apps/gateway/internal/projectmodel/inspect.go
File-SHA256: 7b181d44f7adcbbbb2a93fc565420c326629671b8502503f6386049905523638
Original-Ranges: 129-149,152-223,225-266
Summary: Maven/Gradle marker detection, bounded Maven modules/dependencies, and JDK probe.

```text
129: // Inspect walks the workspace root for Maven/Gradle markers and returns a project snapshot.
130: func Inspect(root string) ProjectInfo {
131: 	info := ProjectInfo{
132: 		Build: BuildNone,
133: 		JDK:   detectJDK(),
134: 	}
135: 	pomPath := filepath.Join(root, "pom.xml")
136: 	if _, err := os.Stat(pomPath); err == nil {
137: 		info.Build = BuildMaven
138: 		fillMaven(&info, root, "", 0)
139: 		return info
140: 	}
141: 	for _, name := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
142: 		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
143: 			info.Build = BuildGradle
144: 			base := filepath.Base(root)
145: 			info.RootModule = &ModuleInfo{Path: "", ArtifactID: base, Name: base}
146: 			return info
147: 		}
148: 	}
149: 	return info
```

```text
152: func fillMaven(info *ProjectInfo, root, rel string, depth int) {
153: 	pomFile := "pom.xml"
154: 	if rel != "" {
155: 		pomFile = filepath.Join(filepath.FromSlash(rel), "pom.xml")
156: 	}
157: 	abs := filepath.Join(root, pomFile)
158: 	pom, err := readPom(abs)
159: 	if err != nil {
160: 		return
161: 	}
162: 	mod := ModuleInfo{
163: 		Path:       rel,
164: 		ArtifactID: pom.ArtifactID,
165: 		GroupID:    pom.effectiveGroup(),
166: 		Version:    pom.effectiveVersion(),
167: 		Packaging:  pom.Packaging,
168: 		Name:       pom.Name,
169: 	}
170: 	if mod.Packaging == "" {
171: 		mod.Packaging = "jar"
172: 	}
173: 	if mod.Name == "" {
[范围中间 42 行未复制；请按原文件与摘要复核]
216: 		}
217: 		child := m
218: 		if rel != "" {
219: 			child = path.Join(rel, m)
220: 		}
221: 		fillMaven(info, root, child, depth+1)
222: 	}
223: }
```

```text
225: var (
226: 	jdkOnce sync.Once
227: 	jdkInfo JDKInfo
228: )
229:
230: func detectJDK() JDKInfo {
231: 	jdkOnce.Do(func() {
232: 		jdkInfo = probeJDK()
233: 	})
234: 	return jdkInfo
235: }
236:
237: func probeJDK() JDKInfo {
238: 	info := JDKInfo{Home: strings.TrimSpace(os.Getenv("JAVA_HOME"))}
239: 	javaBin := "java"
240: 	if info.Home != "" {
241: 		candidate := filepath.Join(info.Home, "bin", "java")
242: 		if _, err := os.Stat(candidate); err == nil {
243: 			javaBin = candidate
244: 		}
245: 	}
246: 	ctx := exec.Command(javaBin, "-XshowSettings:properties", "-version")
[范围中间 12 行未复制；请按原文件与摘要复核]
259: 		cmd := exec.Command(javaBin, "-version")
260: 		var buf bytes.Buffer
261: 		cmd.Stdout = &buf
262: 		cmd.Stderr = &buf
263: 		_ = cmd.Run()
264: 		info.Version = parseJavaVersion(buf.String())
265: 	}
266: 	return info
```

## CODE-04 · standalone-web-idea · apps/gateway/internal/jdtls/manager.go
File-SHA256: d351457dde8e7d839bc5352b212ebe6d4652a4dabfcd065391e8c61e6d195490
Original-Ranges: 94-170,173-188
Summary: Lazy jdtls startup, stdio session, and workspace lifecycle stop.

```text
94: func (m *Manager) Ensure(workspaceID, workspaceRoot string) (*Session, error) {
95: 	if !m.Configured() {
96: 		return nil, ErrNotConfigured
97: 	}
98: 	m.mu.Lock()
99: 	defer m.mu.Unlock()
100: 	if s, ok := m.by[workspaceID]; ok {
101: 		if s.Status == StatusReady || s.Status == StatusStarting {
102: 			return s, nil
103: 		}
104: 		_ = m.stopLocked(workspaceID)
105: 	}
106:
107: 	dataDir := filepath.Join(m.cfg.DataRoot, workspaceID)
108: 	if err := os.MkdirAll(dataDir, 0o755); err != nil {
109: 		return nil, err
110: 	}
111:
112: 	cmd := exec.Command(m.cfg.LaunchScript, dataDir, workspaceRoot)
113: 	cmd.Dir = workspaceRoot
114: 	cmd.Env = append(os.Environ(), "WORKSPACE_ROOT="+workspaceRoot)
115: 	stdin, err := cmd.StdinPipe()
[范围中间 47 行未复制；请按原文件与摘要复核]
163:
164: func (m *Manager) Stop(workspaceID string) {
165: 	m.mu.Lock()
166: 	defer m.mu.Unlock()
167: 	_ = m.stopLocked(workspaceID)
168: }
169:
170: func (m *Manager) stopLocked(workspaceID string) error {
```

```text
173: 		return nil
174: 	}
175: 	delete(m.by, workspaceID)
176: 	if s.Stdin != nil {
177: 		_ = s.Stdin.Close()
178: 	}
179: 	if s.Cmd != nil && s.Cmd.Process != nil {
180: 		_ = s.Cmd.Process.Kill()
181: 		if s.done != nil {
182: 			<-s.done
183: 		} else {
184: 			_, _ = s.Cmd.Process.Wait()
185: 		}
186: 	}
187: 	return nil
188: }
```

## CODE-05 · standalone-web-idea · apps/web/src/App.tsx
File-SHA256: e63332f2622607eedcdd71166ce7dc05505d0b8652cc42a95565c083d6d2af6a
Original-Ranges: 245-280,553-723,840-895
Summary: Standalone save/edit flow, usages/definition navigation, and CodePane wiring.

```text
245:   async function saveFile(): Promise<boolean> {
246:     if (savePromiseRef.current) {
247:       if (!await savePromiseRef.current) return false
248:       return dirtyRef.current ? saveFile() : true
249:     }
250:     const current = sessionRef.current
251:     const path = activePathRef.current
252:     const content = fileContentRef.current
253:     if (!current || !path || content == null || !dirtyRef.current) return true
254:     if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
255:     setSaving(true)
256:     savingRef.current = true
257:     const work = (async () => {
258:       try {
259:         await current.client.writeFile(current.workspaceId, path, content)
260:         if (fileContentRef.current === content && activePathRef.current === path) {
261:           setDirty(false)
262:           dirtyRef.current = false
263:         }
264:         setFileError(null)
265:         ignoreWatchUntilRef.current = Date.now() + 1200
266:         await captureDiskFingerprint(path)
[范围中间 6 行未复制；请按原文件与摘要复核]
273:         setSaving(false)
274:         savingRef.current = false
275:         savePromiseRef.current = null
276:       }
277:     })()
278:     savePromiseRef.current = work
279:     const ok = await work
280:     return ok && dirtyRef.current ? saveFile() : ok
```

```text
553:   async function showUsagesPopup(
554:     req: SymbolClickRequest,
555:     opts?: { skipSingleJump?: boolean; recordFrom?: NavPlace | null },
556:   ) {
557:     const seq = ++peekSeqRef.current
558:     const lsp = lspRef.current
559:     if (!session) return
560:     if (!lsp || lsp.status === 'off' || lsp.status === 'failed') {
561:       setPeekOpen(true); setPeekSymbol(req.word); setPeekAnchor(req.anchor); setPeekHits([]); setPeekLoading(false)
562:       setPeekError('Java navigation is unavailable. You can still use Go to File and Find in Files.')
563:       return
564:     }
565:     setPeekMode('usages')
566:     setPeekOpen(true)
567:     setPeekSymbol(req.word)
568:     setPeekAnchor(req.anchor)
569:     setPeekHits([])
570:     setPeekError(null)
571:     setPeekLoading(true)
572:     setPeekProgress(
573:       lsp.progress || (lsp.status === 'connecting' ? 'Waiting for jdtls index…' : 'Searching…'),
574:     )
[范围中间 141 行未复制；请按原文件与摘要复核]
716:     } catch (err) {
717:       if (seq !== peekSeqRef.current) return
718:       setPeekOpen(true)
719:       setPeekMode('usages')
720:       setPeekLoading(false)
721:       setPeekError(err instanceof Error ? err.message : 'navigation failed')
722:     }
723:   }
```

```text
840:                   <button className="ij-tab-close" aria-label={`Close ${name}`} title="Close file · ⌘/Ctrl+W" onClick={() => void closeTab(path)}>×</button>
841:                 </div>
842:               })}
843:               {previewPath ? <button className="ij-pin-tab" onClick={() => setPreviewPath(null)} title="Keep the preview tab open. You can also double-click it.">Keep open</button> : null}
844:             </div> : <div className="ij-empty-tabs">Editor</div>}
845:             {fileError ? <div className="ij-file-error" role="alert"><span>{fileError}</span>{dirty ? <button className="ij-ghost-btn" onClick={() => void saveFile()}>Retry save</button> : null}<button className="ij-ghost-btn" onClick={() => setFileError(null)} aria-label="Dismiss error">×</button></div> : null}
846:             {!activePath ? <div className="ij-reading-start">
847:               <div className="ij-start-symbol" aria-hidden="true">{ }</div>
848:               <h2>Find the code. Follow the idea.</h2>
849:               <p>A familiar place to understand what changed and how it works.</p>
850:               <button onClick={() => setQuickMode('files')}><span>Go to File</span><kbd>⌘⇧O / Ctrl+P</kbd></button>
851:               <button onClick={() => setQuickMode('recent')}><span>Recent Files</span><kbd>⌘ / Ctrl+E</kbd></button>
852:               <button onClick={() => setFindOpen(true)}><span>Find in Files</span><kbd>⌘ / Ctrl+Shift+F</kbd></button>
853:               <small>In Java: ⌘/Ctrl+B to follow a symbol · Alt+F7 to find usages</small>
854:             </div> : null}
855:             {activePath ? <Suspense fallback={<div className="ij-editor-empty">Loading editor…</div>}><CodePane
856:               path={activePath}
857:               fontSize={fontSize}
858:               wordWrap={wordWrap}
859:               focusRequest={focusRequest}
860:               onCursorChange={(position) => { caretRef.current = position; setCaret(position) }}
861:               onRevealInTree={(directory) => { setRevealDir(directory); setProjectVisible(true) }}
[范围中间 26 行未复制；请按原文件与摘要复核]
888:               }}
889:               onSave={() => void saveFile()}
890:               getLspSession={() => lspRef.current}
891:               reveal={reveal}
892:               mdScrollToId={mdScrollToId}
893:               onOpenFromMarkdown={openFromMarkdown}
894:               onSymbolClick={(req) => void onSymbolClick(req)}
895:             /></Suspense> : null}
```

## CODE-06 · standalone-web-idea · apps/web/src/lsp/session.ts
File-SHA256: 49918225ac98098ac59fe7785defba41a9fdc01be7b2269652628d7b6b6cce75
Original-Ranges: 215-245,371-430
Summary: jdtls initialize settings plus completion, references, and definition requests.

```text
215:         const root = clientRootUri(this.workspaceId)
216:         await rpc.request(
217:           'initialize',
218:           {
219:             processId: null,
220:             clientInfo: { name: 'web-idea', version: '0.1' },
221:             rootUri: root,
222:             capabilities: {
223:               textDocument: {
224:                 definition: { linkSupport: false },
225:                 references: {},
226:                 hover: { contentFormat: ['markdown', 'plaintext'] },
227:                 completion: {
228:                   completionItem: { snippetSupport: false },
229:                   contextSupport: true,
230:                 },
231:                 synchronization: { didSave: true, willSave: false, didChange: true },
232:               },
233:               workspace: { workspaceFolders: true, configuration: true },
234:               window: { workDoneProgress: true },
235:             },
236:             workspaceFolders: [{ uri: root, name: 'workspace' }],
237:             initializationOptions: {
238:               settings: {
239:                 java: {
240:                   import: { maven: { enabled: true }, gradle: { enabled: true } },
241:                   autobuild: { enabled: true },
242:                 },
243:               },
244:             },
245:           },
```

```text
371:   async completion(
372:     relPath: string,
373:     position: LspPosition,
374:     triggerCharacter?: string,
375:   ): Promise<LspCompletionItem[]> {
376:     await this.ensureStarted()
377:     const rpc = this.rpc
378:     if (!rpc || !this.started) throw new Error('LSP not connected')
379:     await this.waitUntilReady()
380:     if (this.rpc !== rpc || !this.started) throw new Error('LSP not connected')
381:     const uri = clientDocUri(this.workspaceId, relPath)
382:     const result = await rpc.request(
383:       'textDocument/completion',
384:       {
385:         textDocument: { uri },
386:         position,
387:         context: triggerCharacter
388:           ? { triggerKind: 2, triggerCharacter }
389:           : { triggerKind: 1 },
390:       },
391:       30_000,
392:     )
[范围中间 30 行未复制；请按原文件与摘要复核]
423:       'textDocument/definition',
424:       {
425:         textDocument: { uri },
426:         position,
427:       },
428:       90_000,
429:     )
430:     return normalizeLocations(result)
```

## CODE-07 · agent-work-fixture · apps/gateway/internal/api/server.go
File-SHA256: 3aff9ffdc6eaafd20c5ce236ab76356a13a4b2472615249f46ae714c9f143895
Original-Ranges: 138-157,182-240,286-310,377-410
Summary: Embedded snapshot Gateway routes, TTL/read-only workspace creation, and jdtls/read-only FS boundary.

```text
138: 	api := http.NewServeMux()
139: 	api.HandleFunc("POST /api/v1/workspaces", s.handleCreateWorkspace)
140: 	api.HandleFunc("GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
141: 	api.HandleFunc("DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
142: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/tree", s.handleTree)
143: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/file", s.handleFile)
144: 	api.HandleFunc("PUT /api/v1/workspaces/{id}/fs/file", s.handlePutFile)
145: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/stat", s.handleStat)
146: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/search", s.handleSearch)
147: 	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/files", s.handleFiles)
148: 	api.HandleFunc("GET /api/v1/workspaces/{id}/project", s.handleProject)
149: 	api.HandleFunc("GET /api/v1/workspaces/{id}/lsp", s.handleLSP)
150:
151: 	protected := auth.DevBearer{Token: s.cfg.DevToken}.Middleware(api)
152: 	mux.Handle("/api/", protected)
153: 	if s.cfg.StaticDir != "" {
154: 		mux.Handle("/", staticFiles(s.cfg.StaticDir))
155: 	}
156:
157: 	return withCORS(s.cfg.CORSOrigins, mux)
```

```text
182: func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
183: 	var req createReq
184: 	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
185: 	if err := dec.Decode(&req); err != nil {
186: 		writeErr(w, http.StatusBadRequest, "invalid json")
187: 		return
188: 	}
189: 	headerKey := strings.TrimSpace(r.Header.Get("X-ATW-Code-Workspace-Create-ID"))
190: 	bodyKey := strings.TrimSpace(req.ClientKey)
191: 	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
192: 		writeErr(w, http.StatusBadRequest, "client key header does not match body")
193: 		return
194: 	}
195: 	clientKey := bodyKey
196: 	if clientKey == "" {
197: 		clientKey = headerKey
198: 	}
199: 	createOptions := workspace.CreateOptions{ClientKey: clientKey}
200: 	if req.TTLSeconds != nil {
201: 		if *req.TTLSeconds < 0 {
202: 			writeErr(w, http.StatusBadRequest, workspace.ErrInvalidTTL.Error())
203: 			return
[范围中间 29 行未复制；请按原文件与摘要复核]
233: 		return
234: 	}
235: 	status := http.StatusCreated
236: 	if replayed {
237: 		status = http.StatusOK
238: 	}
239: 	writeJSON(w, status, s.workspaceJSON(ws))
240: }
```

```text
286: func (s *Server) handleLSP(w http.ResponseWriter, r *http.Request) {
287: 	if s.jdtls == nil || !s.jdtls.Configured() {
288: 		writeErr(w, http.StatusServiceUnavailable, "jdtls not configured (set WEBIDEA_JDTLS_LAUNCH)")
289: 		return
290: 	}
291: 	id := r.PathValue("id")
292: 	ws, err := s.getWorkspace(id)
293: 	if err != nil {
294: 		writeErr(w, http.StatusNotFound, "workspace not found")
295: 		return
296: 	}
297: 	sess, err := s.jdtls.Ensure(id, ws.Root, ws.ReadOnly)
298: 	if err != nil {
299: 		writeErr(w, http.StatusServiceUnavailable, err.Error())
300: 		return
301: 	}
302: 	if !s.acquireLSP(id) {
303: 		writeErr(w, http.StatusConflict, "workspace already has an active lsp connection")
304: 		return
305: 	}
306: 	defer func() {
307: 		s.jdtls.Stop(id)
308: 		s.releaseLSP(id)
309: 	}()
310: 	lspproxy.Handle(w, r, sess, id, ws.Root, ws.ReadOnly)
```

```text
377: func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
378: 	ws, err := s.getWorkspace(r.PathValue("id"))
379: 	if err != nil {
380: 		writeErr(w, http.StatusNotFound, "workspace not found")
381: 		return
382: 	}
383: 	if ws.ReadOnly {
384: 		writeErr(w, http.StatusForbidden, "workspace is read-only")
385: 		return
386: 	}
387: 	rel := r.URL.Query().Get("path")
388: 	if rel == "" {
389: 		writeErr(w, http.StatusBadRequest, "path required")
390: 		return
391: 	}
392: 	max := ws.Jail.MaxFileBytes
393: 	if max <= 0 {
394: 		max = fsjail.DefaultMaxFileBytes
395: 	}
396: 	limited := io.LimitReader(r.Body, max+1)
397: 	data, err := io.ReadAll(limited)
398: 	if err != nil {
[范围中间 4 行未复制；请按原文件与摘要复核]
403: 		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
404: 		return
405: 	}
406: 	if err := ws.Jail.WriteFile(rel, data); err != nil {
407: 		writeFSErr(w, err)
408: 		return
409: 	}
410: 	w.WriteHeader(http.StatusNoContent)
```

## CODE-08 · agent-work-fixture · apps/gateway/internal/lspproxy/proxy.go
File-SHA256: f494ca3eb09e124337f423192d3e4bf03f8add885df1023ae397c8cd12016da0
Original-Ranges: 20-110,115-186
Summary: LSP bridge and read-only JSON-RPC method whitelist.

```text
20: // Handle bridges a browser WebSocket (JSON-RPC text frames) to jdtls stdio.
21: func Handle(w http.ResponseWriter, r *http.Request, sess *jdtls.Session, workspaceID, root string, readOnly bool) {
22: 	conn, err := upgrader.Upgrade(w, r, nil)
23: 	if err != nil {
24: 		log.Printf("lspproxy upgrade: %v", err)
25: 		return
26: 	}
27: 	defer conn.Close()
28: 	conn.SetReadLimit(MaxFrameBytes)
29:
30: 	var writeMu sync.Mutex
31: 	errCh := make(chan error, 2)
32: 	handlerDone := make(chan struct{})
33: 	defer close(handlerDone)
34:
35: 	// A workspace DELETE stops the session and closes Done after the process
36: 	// exits. Closing the WebSocket here also unblocks the client reader when a
37: 	// browser connection is still open during that shutdown.
38: 	go func() {
39: 		select {
40: 		case <-sess.Done():
41: 			_ = conn.Close()
[范围中间 61 行未复制；请按原文件与摘要复核]
103: 				continue
104: 			}
105: 			if err := WriteFrame(sess.Stdin, out); err != nil {
106: 				errCh <- err
107: 				return
108: 			}
109: 		}
110: 	}()
```

```text
115: var readOnlyMethods = map[string]struct{}{
116: 	"initialize":                       {},
117: 	"initialized":                      {},
118: 	"shutdown":                         {},
119: 	"exit":                             {},
120: 	"textDocument/didOpen":             {},
121: 	"textDocument/didChange":           {},
122: 	"textDocument/didClose":            {},
123: 	"textDocument/didSave":             {},
124: 	"textDocument/definition":          {},
125: 	"textDocument/declaration":         {},
126: 	"textDocument/typeDefinition":      {},
127: 	"textDocument/implementation":      {},
128: 	"textDocument/references":          {},
129: 	"textDocument/hover":               {},
130: 	"textDocument/completion":          {},
131: 	"textDocument/signatureHelp":       {},
132: 	"textDocument/documentSymbol":      {},
133: 	"textDocument/semanticTokens/full": {},
134: 	"workspace/symbol":                 {},
135: 	"$/cancelRequest":                  {},
136: 	"window/workDoneProgress/cancel":   {},
[范围中间 42 行未复制；请按原文件与摘要复核]
179: 	}
180: 	if hasResult && len(bytes.TrimSpace(result)) == 0 {
181: 		return false, nil
182: 	}
183: 	if hasError && !validRPCError(errValue) {
184: 		return false, nil
185: 	}
186: 	return true, nil
```

## CODE-09 · agent-work-fixture · apps/gateway/internal/projectmodel/inspect.go
File-SHA256: 98ae657631b74bc912eb444554be7753e7413fd5a48a2a4527ce52c614f941d8
Original-Ranges: 129-149,152-223,225-266
Summary: Same lightweight Maven/Gradle/JDK project snapshot used by the embedded reader.

```text
129: // Inspect walks the workspace root for Maven/Gradle markers and returns a project snapshot.
130: func Inspect(root string) ProjectInfo {
131: 	info := ProjectInfo{
132: 		Build: BuildNone,
133: 		JDK:   detectJDK(),
134: 	}
135: 	pomPath := filepath.Join(root, "pom.xml")
136: 	if _, err := os.Stat(pomPath); err == nil {
137: 		info.Build = BuildMaven
138: 		fillMaven(&info, root, "", 0)
139: 		return info
140: 	}
141: 	for _, name := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
142: 		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
143: 			info.Build = BuildGradle
144: 			base := filepath.Base(root)
145: 			info.RootModule = &ModuleInfo{Path: "", ArtifactID: base, Name: base}
146: 			return info
147: 		}
148: 	}
149: 	return info
```

```text
152: func fillMaven(info *ProjectInfo, root, rel string, depth int) {
153: 	pomFile := "pom.xml"
154: 	if rel != "" {
155: 		pomFile = filepath.Join(filepath.FromSlash(rel), "pom.xml")
156: 	}
157: 	abs := filepath.Join(root, pomFile)
158: 	pom, err := readPom(abs)
159: 	if err != nil {
160: 		return
161: 	}
162: 	mod := ModuleInfo{
163: 		Path:       rel,
164: 		ArtifactID: pom.ArtifactID,
165: 		GroupID:    pom.effectiveGroup(),
166: 		Version:    pom.effectiveVersion(),
167: 		Packaging:  pom.Packaging,
168: 		Name:       pom.Name,
169: 	}
170: 	if mod.Packaging == "" {
171: 		mod.Packaging = "jar"
172: 	}
173: 	if mod.Name == "" {
[范围中间 42 行未复制；请按原文件与摘要复核]
216: 		}
217: 		child := m
218: 		if rel != "" {
219: 			child = path.Join(rel, m)
220: 		}
221: 		fillMaven(info, root, child, depth+1)
222: 	}
223: }
```

```text
225: var (
226: 	jdkOnce sync.Once
227: 	jdkInfo JDKInfo
228: )
229:
230: func detectJDK() JDKInfo {
231: 	jdkOnce.Do(func() {
232: 		jdkInfo = probeJDK()
233: 	})
234: 	return jdkInfo
235: }
236:
237: func probeJDK() JDKInfo {
238: 	info := JDKInfo{Home: strings.TrimSpace(os.Getenv("JAVA_HOME"))}
239: 	javaBin := "java"
240: 	if info.Home != "" {
241: 		candidate := filepath.Join(info.Home, "bin", "java")
242: 		if _, err := os.Stat(candidate); err == nil {
243: 			javaBin = candidate
244: 		}
245: 	}
246: 	ctx := exec.Command(javaBin, "-XshowSettings:properties", "-version")
[范围中间 12 行未复制；请按原文件与摘要复核]
259: 		cmd := exec.Command(javaBin, "-version")
260: 		var buf bytes.Buffer
261: 		cmd.Stdout = &buf
262: 		cmd.Stderr = &buf
263: 		_ = cmd.Run()
264: 		info.Version = parseJavaVersion(buf.String())
265: 	}
266: 	return info
```

## CODE-10 · agent-work-fixture · apps/gateway/internal/jdtls/manager.go
File-SHA256: 5507279a8bb62463795870d9ba8a0140b70e11f904e1793c96bd9908ef77cec9
Original-Ranges: 94-170,173-248
Summary: Snapshot jdtls lifecycle and per-workspace data cleanup.

```text
94: // Ensure starts jdtls for the workspace if needed. Returns ErrNotConfigured when disabled.
95: // The optional readOnly flag is used by the sidecar launcher to keep Eclipse
96: // project metadata in the jdtls data area instead of the checkout.
97: func (m *Manager) Ensure(workspaceID, workspaceRoot string, readOnly ...bool) (*Session, error) {
98: 	if !m.Configured() {
99: 		return nil, ErrNotConfigured
100: 	}
101: 	m.mu.Lock()
102: 	defer m.mu.Unlock()
103: 	if s, ok := m.by[workspaceID]; ok {
104: 		if s.Status == StatusReady || s.Status == StatusStarting {
105: 			return s, nil
106: 		}
107: 		_ = m.stopLocked(workspaceID, false)
108: 	}
109:
110: 	dataDir, err := m.workspaceDataDir(workspaceID)
111: 	if err != nil {
112: 		return nil, err
113: 	}
114: 	if err := os.MkdirAll(dataDir, 0o755); err != nil {
115: 		return nil, err
[范围中间 47 行未复制；请按原文件与摘要复核]
163: 		if err != nil {
164: 			cur.Err = err.Error()
165: 		} else {
166: 			cur.Err = "jdtls exited"
167: 		}
168: 	}()
169:
170: 	return s, nil
```

```text
173: func (m *Manager) Stop(workspaceID string) {
174: 	m.mu.Lock()
175: 	defer m.mu.Unlock()
176: 	_ = m.stopLocked(workspaceID, false)
177: }
178:
179: // StopAndCleanup releases the session and removes only its workspace-owned
180: // data directory. A transient WebSocket disconnect uses Stop so a reconnect
181: // can reuse the index; DELETE uses this method.
182: func (m *Manager) StopAndCleanup(workspaceID string) {
183: 	m.mu.Lock()
184: 	defer m.mu.Unlock()
185: 	_ = m.stopLocked(workspaceID, true)
186: }
187:
188: // StopAll releases every workspace session owned by this manager. It is used
189: // during Gateway shutdown so a supervisor never leaves a jdtls child behind.
190: func (m *Manager) StopAll() {
191: 	m.mu.Lock()
192: 	ids := make([]string, 0, len(m.by))
193: 	for id := range m.by {
194: 		ids = append(ids, id)
[范围中间 46 行未复制；请按原文件与摘要复核]
241: 			return err
242: 		}
243: 		if dataDir != "" {
244: 			return os.RemoveAll(dataDir)
245: 		}
246: 	}
247: 	return nil
248: }
```

## CODE-11 · agent-work-fixture · apps/web/src/App.tsx
File-SHA256: aecf9906955579ed0e0cec4f21c52b5ab6c57096c5278515f626a6da29ffd70c
Original-Ranges: 242-257,341-376,978-1000
Summary: Embedded bootstrap sets readOnly=true; copied standalone save path is guarded and editor callbacks are disabled.

```text
242:   function loadEmbeddedWorkspace(): void {
243:     if (!embeddedWindow) return
244:     setEmbeddedState('loading')
245:     setEmbeddedError(null)
246:     const pending = embeddedBootstrapRef.current ?? (embeddedBootstrapRef.current = fetchEmbeddedBootstrap())
247:     void pending.then((bootstrap) => {
248:       if (!embeddedAppliedRef.current || sessionRef.current?.workspaceId !== bootstrap.workspace.id) {
249:         embeddedAppliedRef.current = true
250:         const nextClient = new GatewayClient(bootstrap.gateway_base_url, '')
251:         applyWorkspaceSession(bootstrap.workspace, nextClient, {
252:           gatewayUrl: bootstrap.gateway_base_url,
253:           token: '',
254:           rootLabel: bootstrap.workspace.root_display || 'Java workspace',
255:           readOnly: true,
256:           embedded: true,
257:         })
```

```text
341:   async function saveFile(): Promise<boolean> {
342:     if (savePromiseRef.current) {
343:       if (!await savePromiseRef.current) return false
344:       return dirtyRef.current ? saveFile() : true
345:     }
346:     const current = sessionRef.current
347:     const path = activePathRef.current
348:     const content = fileContentRef.current
349:     if (!current || current.readOnly || !path || content == null || !dirtyRef.current) return true
350:     if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
351:     setSaving(true)
352:     savingRef.current = true
353:     const work = (async () => {
354:       try {
355:         await current.client.writeFile(current.workspaceId, path, content)
356:         if (fileContentRef.current === content && activePathRef.current === path) {
357:           setDirty(false)
358:           dirtyRef.current = false
359:         }
360:         setFileError(null)
361:         ignoreWatchUntilRef.current = Date.now() + 1200
362:         await captureDiskFingerprint(path)
[范围中间 6 行未复制；请按原文件与摘要复核]
369:         setSaving(false)
370:         savingRef.current = false
371:         savePromiseRef.current = null
372:       }
373:     })()
374:     savePromiseRef.current = work
375:     const ok = await work
376:     return ok && dirtyRef.current ? saveFile() : ok
```

```text
978:               readOnly={session.readOnly}
979:               editorTheme={editorTheme === 'dark' ? 'idea-dark' : 'idea-light'}
980:               onChange={session.readOnly ? undefined : (text) => {
981:                 if (loadingRef.current || !activePathRef.current) return
982:                 setPreviewPath((p) => p === activePathRef.current ? null : p)
983:                 setFileContent(text)
984:                 fileContentRef.current = text
985:                 setDirty(true)
986:                 dirtyRef.current = true
987:                 const path = activePathRef.current
988:                 const lsp = lspRef.current
989:                 if (lsp && path?.endsWith('.java')) {
990:                   if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
991:                   didChangeTimer.current = setTimeout(() => {
992:                     void lsp.didChange(path, text)
993:                   }, 200)
994:                 }
995:                 if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
996:                 autoSaveTimer.current = setTimeout(() => {
997:                   void saveFile()
998:                 }, 800)
999:               }}
1000:               onSave={session.readOnly ? undefined : () => void saveFile()}
```

## CODE-12 · agent-work-fixture · apps/web/src/lsp/session.ts
File-SHA256: 6200634c3b3ae9165bc06bae8aa0dfbb42e917edf1b020af7799879e0efdb262
Original-Ranges: 215-245,368-431
Summary: Embedded LSP session retains completion/references/definition while passing read-only settings.

```text
215:           if (msg) this.setProgress(msg)
216:         })
217:         rpc.onNotification('window/logMessage', () => {})
218:
219:         const root = clientRootUri(this.workspaceId)
220:         await rpc.request(
221:           'initialize',
222:           {
223:             processId: null,
224:             clientInfo: { name: 'web-idea', version: '0.1' },
225:             rootUri: root,
226:             capabilities: {
227:               textDocument: {
228:                 definition: { linkSupport: false },
229:                 references: {},
230:                 hover: { contentFormat: ['markdown', 'plaintext'] },
231:                 completion: {
232:                   completionItem: { snippetSupport: false },
233:                   contextSupport: true,
234:                 },
235:                 synchronization: { didSave: true, willSave: false, didChange: true },
236:               },
237:               workspace: { workspaceFolders: true, configuration: true },
238:               window: { workDoneProgress: true },
239:             },
240:             workspaceFolders: [{ uri: root, name: 'workspace' }],
241:             initializationOptions: { settings: javaLspSettings(this.readOnly) },
242:           },
243:           120_000,
244:         )
245:         if (!this.isCurrent(generation, rpc)) throw new Error('LSP session cancelled')
```

```text
368:   async completion(
369:     relPath: string,
370:     position: LspPosition,
371:     triggerCharacter?: string,
372:   ): Promise<LspCompletionItem[]> {
373:     await this.ensureStarted()
374:     const rpc = this.rpc
375:     if (!rpc || !this.started) throw new Error('LSP not connected')
376:     await this.waitUntilReady()
377:     if (this.rpc !== rpc || !this.started) throw new Error('LSP not connected')
378:     const uri = clientDocUri(this.workspaceId, relPath)
379:     const result = await rpc.request(
380:       'textDocument/completion',
381:       {
382:         textDocument: { uri },
383:         position,
384:         context: triggerCharacter
385:           ? { triggerKind: 2, triggerCharacter }
386:           : { triggerKind: 1 },
387:       },
388:       30_000,
389:     )
[范围中间 34 行未复制；请按原文件与摘要复核]
424:       },
425:       90_000,
426:     )
427:     return normalizeLocations(result)
428:   }
429:
430:   dispose(): void {
431:     const error = new Error('LSP session disposed')
```
