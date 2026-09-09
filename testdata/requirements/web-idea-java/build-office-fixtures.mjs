import fs from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { Presentation, PresentationFile } from "@oai/artifact-tool";

const workspaceDir = process.cwd();
const outputDir = path.join(workspaceDir, "testdata/requirements/web-idea-java/inputs");
const buildDir = process.env.FIXTURE_BUILD_DIR || "/tmp/web-idea-office-fixture-build";
const skillDir = "/Users/yin/.codex/plugins/cache/openai-primary-runtime/presentations/26.905.11957/skills/presentations";
const { resolvePresentationFont } = await import(
  pathToFileURL(path.join(skillDir, "container_tools/artifact_tool_utils.mjs")).href,
);

await fs.mkdir(outputDir, { recursive: true });
await fs.mkdir(buildDir, { recursive: true });
const family = resolvePresentationFont();

const marker = "模拟测试材料｜未获用户确认";
const sourceId = "SRC-PPTX-01";
const colors = {
  background: "#F7F8FA",
  ink: "#172B4D",
  body: "#344054",
  muted: "#667085",
  accent: "#2F5D8C",
  pending: "#9A3412",
};

function box(slide, text, position, style = {}) {
  const shape = slide.shapes.add({
    geometry: "textbox",
    position,
    fill: "none",
    line: { fill: "none", width: 0 },
  });
  shape.text = text;
  shape.text.style = {
    typeface: family,
    fontSize: style.fontSize ?? 22,
    bold: style.bold ?? false,
    color: style.color ?? colors.body,
    align: style.align ?? "left",
    verticalAlign: style.verticalAlign ?? "top",
    autoFit: "shrinkTextOnOverflow",
  };
  return shape;
}

function addHeader(slide) {
  box(slide, marker, { left: 72, top: 28, width: 760, height: 28 }, {
    fontSize: 15, bold: true, color: colors.pending,
  });
  box(slide, sourceId, { left: 1030, top: 28, width: 178, height: 28 }, {
    fontSize: 14, bold: true, color: colors.muted, align: "right",
  });
}

function addTitle(slide, title, subtitle) {
  box(slide, title, { left: 72, top: 92, width: 1136, height: 62 }, {
    fontSize: 40, bold: true, color: colors.ink,
  });
  box(slide, subtitle, { left: 76, top: 162, width: 1100, height: 34 }, {
    fontSize: 18, color: colors.muted,
  });
}

function addRule(slide, top) {
  slide.shapes.add({
    geometry: "line",
    position: { left: 76, top, width: 1128, height: 0 },
    line: { fill: colors.accent, width: 2 },
  });
}

const presentation = Presentation.create({ slideSize: { width: 1280, height: 720 } });

{
  const slide = presentation.slides.add();
  slide.background.fill = colors.background;
  addHeader(slide);
  addTitle(slide, "Java 编辑能力评审提议", "模拟提议 · 需求状态 pending · 文档 ID SRC-PPTX-01");
  addRule(slide, 216);
  box(slide, "模拟提议", { left: 78, top: 258, width: 260, height: 42 }, {
    fontSize: 24, bold: true, color: colors.accent,
  });
  box(slide, "首期包括跨文件 Rename 和 Quick Fix，一键应用并保存", {
    left: 78, top: 316, width: 1040, height: 94,
  }, { fontSize: 31, bold: true, color: colors.ink });
  box(slide, "提议状态：pending\n本页内容仅供评审，未获用户确认。", {
    left: 80, top: 466, width: 620, height: 80,
  }, { fontSize: 21, color: colors.pending });
  box(slide, "测试解析关注点\n动作：跨文件 Rename、Quick Fix\n结果：一键应用并保存", {
    left: 760, top: 456, width: 420, height: 118,
  }, { fontSize: 20, color: colors.body });
  slide.speakerNotes.textFrame.setText(`${marker} · ${sourceId} · 所有内容保持 pending。`);
}

{
  const slide = presentation.slides.add();
  slide.background.fill = colors.background;
  addHeader(slide);
  addTitle(slide, "未决点", "以下问题均保持 pending，不代表已确认的产品规则");
  addRule(slide, 216);
  const items = [
    "预览是否必须",
    "是否需要人审",
    "如何处理修改冲突",
    "失败是否回滚",
    "JDK 版本范围",
  ];
  items.forEach((item, index) => {
    const top = 258 + index * 72;
    box(slide, `${index + 1}`, { left: 84, top, width: 42, height: 42 }, {
      fontSize: 24, bold: true, color: colors.accent, align: "center",
    });
    box(slide, item, { left: 154, top: top - 2, width: 640, height: 48 }, {
      fontSize: 25, bold: true, color: colors.ink,
    });
    box(slide, "pending", { left: 960, top: top + 2, width: 180, height: 38 }, {
      fontSize: 19, bold: true, color: colors.pending, align: "right",
    });
  });
  box(slide, "模拟测试材料｜未获用户确认\nSRC-PPTX-01", {
    left: 78, top: 628, width: 500, height: 46,
  }, { fontSize: 15, color: colors.muted });
  slide.speakerNotes.textFrame.setText(`${marker} · ${sourceId} · 未决点逐项保持 pending。`);
}

const candidatePath = path.join(buildDir, "review-proposal.candidate.pptx");
await (await PresentationFile.exportPptx(presentation)).save(candidatePath);
for (let i = 0; i < presentation.slides.items.length; i += 1) {
  const slide = presentation.slides.items[i];
  const preview = await presentation.export({ slide, format: "png", scale: 1 });
  await fs.writeFile(path.join(buildDir, `review-proposal-slide-${i + 1}.png`), new Uint8Array(await preview.arrayBuffer()));
  const layout = await slide.export({ format: "layout" });
  await fs.writeFile(path.join(buildDir, `review-proposal-slide-${i + 1}.layout.json`), await layout.text());
}
console.log(candidatePath);
