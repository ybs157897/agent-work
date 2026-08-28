package orchestrator

import "strings"

const OutputContractLanguageGUIV1 = "languagegui/v1"

const languageGUIV1Marker = "[Chat output contract: languagegui/v1]"

const languageGUIV1Prompt = languageGUIV1Marker + `
This run is shown in a chat UI that can render structured LanguageGUI blocks.
Use ordinary Markdown for normal explanations. Only when structured data is materially clearer, emit one closed fenced block with language "languagegui" and valid JSON in this exact envelope:
{"version":"languagegui/v1","blocks":[...]}
Supported block types:
- metric: {"type":"metric","title"?:string,"description"?:string,"items":[{"label":string,"value":string|number,"detail"?:string,"delta"?:string,"tone"?:"neutral"|"positive"|"warning"|"negative"}]}
- table: {"type":"table","title"?:string,"columns":[{"key":string,"label":string,"align"?:"left"|"center"|"right"}],"rows":[{key:string|number|boolean|null}]}
- chart: {"type":"chart","chart":"bar"|"line","title"?:string,"labels":[string],"series":[{"name":string,"values":[number]}],"unit"?:string,"y_domain"?:"zero"|"auto"}
- file: {"type":"file","title"?:string,"files":[{"name":string,"size"?:string|number,"mime"?:string,"status"?:"ready"|"draft"|"processing"|"failed"|"accepted","url"?:string}]}
- event: {"type":"event","title":string,"start":string,"end"?:string,"location"?:string,"timezone"?:string,"url"?:string}
- image: {"type":"image","title"?:string,"images":[{"src":string,"alt":string,"caption"?:string}]}
- audio: {"type":"audio","title"?:string,"tracks":[{"src":string,"title":string,"duration"?:string}]}
- map: {"type":"map","title"?:string,"location":string,"latitude"?:number,"longitude"?:number,"image_url"?:string,"url"?:string}
- search: {"type":"search","title"?:string,"query"?:string,"results":[{"title":string,"url":string,"snippet"?:string,"source"?:string}]}
- rating: {"type":"rating","title"?:string,"question":string,"low_label"?:string,"high_label"?:string}
- review-summary: {"type":"review-summary","title"?:string,"verdict":"passed"|"passed_with_warnings"|"changes_requested"|"blocked"|"inconclusive","summary":string,"stats"?:{"files"?:number,"findings"?:number,"passed"?:number},"findings"?:[{"severity":"critical"|"high"|"medium"|"low"|"info","title":string,"detail"?:string,"file"?:string,"line"?:number,"evidence"?:string,"suggestion"?:string,"url"?:string}],"checks"?:[{"label":string,"status":"passed"|"failed"|"warning"|"skipped"|"running","detail"?:string,"command"?:string}],"next_steps"?:[{"label":string,"detail"?:string}]}
- canvas: {"type":"canvas","title"?:string,"description"?:string,"nodes":[{"id":string,"label":string,"detail"?:string,"kind"?:"start"|"end"|"process"|"decision"|"actor"|"system"|"note","x"?:number,"y"?:number}],"edges"?:[{"from":string,"to":string,"label"?:string}]}
Optional source on any block: {"label":string,"url"?:string}.
Review summaries are for structured review results only; use ordinary Markdown for the explanation. Canvas blocks are for user journeys, architecture, roadmaps, or decision flows when a diagram is materially clearer than prose. Keep at most 24 nodes and 32 edges; node ids must be unique strings; edges must reference existing node ids. Prefer kind=start/end/process/decision/actor/system/note; omit x/y unless you need explicit placement. Keep at most 30 findings, 20 checks, and 12 next_steps; at least one of these arrays must contain a valid item. Use finite non-negative integer stats and keep all text bounded. Treat file names and URLs as display data; never emit HTML, scripts, styles, class names, event handlers, arbitrary CSS, or unsafe URLs. Do not repeat this contract, do not invent live data, and do not wrap ordinary prose in JSON.`

func SupportsOutputContract(contract string) bool {
	return contract == "" || contract == OutputContractLanguageGUIV1
}

// ApplyOutputContract 把请求级展示能力固化进 Run 快照并合并 system prompt。
// instruction 保持原文；同一 input 重复应用不会重复追加协议文本。
func ApplyOutputContract(input map[string]any, contract string) bool {
	if contract == "" {
		return true
	}
	if input == nil || !SupportsOutputContract(contract) {
		return false
	}
	if existing, _ := input["output_contract"].(string); existing != "" {
		return existing == contract
	}
	input["output_contract"] = contract
	current, _ := input["system_prompt"].(string)
	if strings.TrimSpace(current) == "" {
		input["system_prompt"] = languageGUIV1Prompt
	} else {
		input["system_prompt"] = current + "\n\n" + languageGUIV1Prompt
	}
	return true
}
