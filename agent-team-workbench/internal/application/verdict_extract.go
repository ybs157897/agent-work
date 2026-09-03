package application

import "strings"

// extractLastFencedBlock is retained only for the legacy evaluation verdict
// artifact format. PlanDecisionV2 control output is raw JSON and never uses
// this helper.
func extractLastFencedBlock(text, name string) (string, bool) {
	lines := strings.Split(text, "\n")
	var content []string
	inBlock, found := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "```") &&
				strings.TrimSpace(strings.TrimPrefix(trimmed, "```")) == name {
				inBlock, found = true, true
				content = nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inBlock = false
			continue
		}
		content = append(content, line)
	}
	if !found {
		return "", false
	}
	return strings.Join(content, "\n"), true
}
