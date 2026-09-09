# web-idea Java requirement fixture QA

Date: 2026-09-08

## Outputs

- `testdata/requirements/web-idea-java/inputs/reading-scope.docx`
  - Document ID: `SRC-DOCX-01`
  - Two rendered pages
  - Repeating header and footer visibly mark `模拟测试材料｜未获用户确认`
- `testdata/requirements/web-idea-java/inputs/review-proposal.pptx`
  - Presentation ID: `SRC-PPTX-01`
  - Two slides
  - Every slide visibly marks `模拟测试材料｜未获用户确认`
- `reading-scope.expected.txt` and `review-proposal.expected.txt` retain page/slide boundaries and expected extractable text. They describe parser expectations only and do not claim that ATW currently supports these inputs.

Both fixtures contain no external media. The DOCX explicitly includes the fact block requested by the task, the four requested exception cases, the statement `这不是现行业务规则。`, and pending confirmation items. The PPTX keeps the Rename/Quick Fix proposal and all five unresolved points in `pending` state.

## Authoring and validation

Workspace dependencies were loaded through `load_workspace_dependencies`. Authoring used the bundled Python runtime and `python-docx` for DOCX, and the bundled Node runtime with `@oai/artifact-tool` for PPTX. No packages were installed.

DOCX command:

```text
/Users/yin/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
  testdata/requirements/web-idea-java/build-office-fixtures.py \
  --output testdata/requirements/web-idea-java/inputs/reading-scope.docx
```

PPTX was built from `build-office-fixtures.mjs` into a private `/tmp/web-idea-office-fixture-build` directory. The finalizer report is `/tmp/web-idea-office-fixture-pptx-validation.json`: package integrity passed, 2 slides were imported successfully by Artifact Tool, layout findings were 0, and the observed font family was `Helvetica Neue`.

Text extraction checks passed for all required DOCX terms: title, document ID, simulation marker, scope fact block, index/JDK/zero-or-multiple-result/read-only dependency exceptions, and `这不是现行业务规则`. PPTX ZIP text checks passed for the title, proposal, five pending points, marker, and ID; the final package contains 2 slide XML files.

## Render review

- DOCX rendered with the bundled LibreOffice executable at `/Users/yin/.cache/codex-runtimes/codex-primary-runtime/dependencies/bin/override/soffice` through `render_docx.py`. Final render: `/tmp/web-idea-office-fixture-docx-render4/page-1.png` and `page-2.png`. Both pages were inspected at high resolution. Text, tables, markers, spacing, and page break are readable with no clipping or overlap.
- DOCX headless rendering required a temporary Fontconfig file and a copied system `PingFang.ttc` under `/tmp/web-idea-office-fonts`, because the isolated LibreOffice runtime did not expose Chinese glyphs by default. The artifact declares `PingFang SC`; this is a render-environment limitation, not an extra fixture dependency.
- PPTX final slides were rendered from the finalized PPTX to `/tmp/web-idea-office-fixture-final-render/slide-1.png` and `slide-2.png` with Artifact Tool and inspected individually. Both slides are clean, readable, and keep every proposal explicitly pending.

No user confirmation, follow-up event, implementation claim, or real business rule was added to either fixture.
