package application

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeFinishGateResult is the control-plane verdict for a model finish
// proposal. A model may say "complete", but only the application can prove
// that every required coverage subject, discovered relation, and visible
// endpoint was delivered with evidence.
type KnowledgeFinishGateResult struct {
	Status               domain.KnowledgeCoverageStatus
	Gaps                 []string
	MissingItemIDs       []string
	MissingRelationIDs   []string
	UnknownEvidenceIDs   []string
	CoverageSubjectsMiss []string
}

// KnowledgeFinishGate enforces the finite, machine-checkable part of the
// librarian contract. It intentionally remains a static gate: it cannot
// prove that the model understood the domain, but it can prevent a response
// that silently drops a relation endpoint discovered by the Harness.
func KnowledgeFinishGate(job *domain.KnowledgeJob, finish *KnowledgeFinishDecision) KnowledgeFinishGateResult {
	result := KnowledgeFinishGateResult{Status: domain.KnowledgeCoverageComplete}
	if finish == nil {
		result.Status = domain.KnowledgeCoverageMissing
		result.Gaps = append(result.Gaps, "缺少 finish 结构化结果")
		return result
	}
	result.Status = finish.Status
	if result.Status == "" {
		result.Status = domain.KnowledgeCoverageMissing
	}
	if job == nil {
		result.Status = domain.KnowledgeCoverageMissing
		result.Gaps = append(result.Gaps, "缺少知识作业状态")
		return result
	}

	knownEvidence := make(map[string]struct{}, len(job.EvidenceIDs))
	for _, id := range job.EvidenceIDs {
		if id != "" {
			knownEvidence[id] = struct{}{}
		}
	}
	finishEvidence := make(map[string]struct{}, len(finish.EvidenceIDs))
	for _, id := range finish.EvidenceIDs {
		if id == "" {
			continue
		}
		finishEvidence[id] = struct{}{}
		if _, ok := knownEvidence[id]; !ok {
			result.UnknownEvidenceIDs = appendUniqueString(result.UnknownEvidenceIDs, id)
		}
	}
	for _, citation := range finish.Citations {
		if citation.VersionID != "" {
			visited := false
			for _, visitedID := range job.VisitedVersionIDs {
				if visitedID == citation.VersionID {
					visited = true
					break
				}
			}
			if !visited {
				result.Gaps = append(result.Gaps, "finish 引用了未实际读取的版本 "+citation.VersionID)
			}
		}
		if citation.SourceID != "" {
			if _, ok := knownEvidence[citation.SourceID]; !ok {
				result.UnknownEvidenceIDs = appendUniqueString(result.UnknownEvidenceIDs, citation.SourceID)
			}
		}
	}
	for _, id := range result.UnknownEvidenceIDs {
		result.Gaps = append(result.Gaps, "finish 引用了未实际读取的证据 "+id)
	}

	// All six subjects are required for a complete investigation. The model's
	// output is merged by the caller; this function validates the proposal in
	// isolation so a missing subject cannot be masked by an older turn.
	coverage := make(map[string]domain.KnowledgeCoverageEntry, len(finish.Coverage))
	for _, entry := range finish.Coverage {
		if !isKnowledgeCoverageSubject(entry.Subject) {
			result.Gaps = append(result.Gaps, "finish 包含未知 coverage subject "+entry.Subject)
			continue
		}
		if _, exists := coverage[entry.Subject]; exists {
			result.Gaps = append(result.Gaps, "finish 重复 coverage subject "+entry.Subject)
			continue
		}
		coverage[entry.Subject] = entry
	}
	for _, subject := range knowledgeCoverageSubjects {
		entry, ok := coverage[subject]
		if !ok || entry.Status != domain.KnowledgeCoverageComplete {
			result.CoverageSubjectsMiss = append(result.CoverageSubjectsMiss, subject)
			result.Gaps = append(result.Gaps, "未完成 coverage subject "+subject)
			continue
		}
		if len(entry.EvidenceIDs) == 0 {
			result.CoverageSubjectsMiss = append(result.CoverageSubjectsMiss, subject)
			result.Gaps = append(result.Gaps, "coverage subject "+subject+" 缺少证据")
			continue
		}
		for _, evidenceID := range entry.EvidenceIDs {
			if _, ok := knownEvidence[evidenceID]; !ok {
				result.Gaps = append(result.Gaps, "coverage subject "+subject+" 引用未读取证据 "+evidenceID)
			}
		}
	}

	// A relation discovered by the control plane creates two obligations: the
	// edge itself must be listed, and its endpoint must be present in a citation
	// or relation payload. Requiring exact edge identity prevents a same-shaped
	// unrelated relation from satisfying A -> B by accident.
	relations := make([]domain.KnowledgeRelationInput, 0, len(finish.Relations))
	for _, relation := range finish.Relations {
		relations = append(relations, relation)
	}
	for _, required := range job.RequiredRelations {
		matched := false
		for _, relation := range relations {
			from := relation.FromItemID
			if from != required.FromItemID || relation.ToItemID != required.ToItemID || relation.Kind != required.Kind {
				continue
			}
			matched = true
			if len(relation.SourceIDs) == 0 || !hasKnownEvidence(relation.SourceIDs, knownEvidence, finishEvidence) {
				result.Gaps = append(result.Gaps, "已知关系 "+knowledgeRelationLabel(required)+" 缺少实际证据")
			}
			break
		}
		if !matched {
			result.MissingRelationIDs = appendUniqueString(result.MissingRelationIDs, required.ID)
			result.Gaps = append(result.Gaps, "未交付已知关系 "+knowledgeRelationLabel(required))
		}
	}

	visitedVersions := make(map[string]struct{}, len(job.VisitedVersionIDs))
	for _, id := range job.VisitedVersionIDs {
		visitedVersions[id] = struct{}{}
	}
	for _, itemID := range job.RequiredItemIDs {
		if itemID == "" {
			continue
		}
		if !knowledgeFinishMentionsItem(finish, itemID, knownEvidence, finishEvidence, visitedVersions) {
			result.MissingItemIDs = appendUniqueString(result.MissingItemIDs, itemID)
			result.Gaps = append(result.Gaps, "未交付已知关联条目 "+itemID)
		}
	}

	if job.Coverage.Truncated {
		result.Gaps = append(result.Gaps, "调查结果已被预算或输出大小截断")
	}
	if result.Status == domain.KnowledgeCoverageComplete && len(result.Gaps) > 0 {
		result.Status = domain.KnowledgeCoveragePartial
	}
	if result.Status == domain.KnowledgeCoverageComplete && len(result.UnknownEvidenceIDs) > 0 {
		result.Status = domain.KnowledgeCoveragePartial
	}
	// A finish marked partial/missing/conflict remains so even if the static
	// checks have no additional findings. Never promote a weaker model status.
	return result
}

func isKnowledgeCoverageSubject(subject string) bool {
	for _, candidate := range knowledgeCoverageSubjects {
		if subject == candidate {
			return true
		}
	}
	return false
}

func knowledgeFinishMentionsItem(finish *KnowledgeFinishDecision, itemID string,
	knownEvidence, finishEvidence, visitedVersions map[string]struct{}) bool {
	for _, relation := range finish.Relations {
		if relation.FromItemID == itemID || relation.ToItemID == itemID {
			if hasKnownEvidence(relation.SourceIDs, knownEvidence, finishEvidence) {
				return true
			}
		}
	}
	for _, citation := range finish.Citations {
		if citation.ItemID != itemID || citation.VersionID == "" || citation.SourceID == "" {
			continue
		}
		if _, visited := visitedVersions[citation.VersionID]; !visited {
			continue
		}
		if _, ok := knownEvidence[citation.SourceID]; ok {
			return true
		}
	}
	return false
}

func hasKnownEvidence(ids []string, knownEvidence, finishEvidence map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := knownEvidence[id]; ok {
			if len(finishEvidence) == 0 {
				return true
			}
			if _, listed := finishEvidence[id]; listed {
				return true
			}
		}
	}
	return false
}

func knowledgeRelationLabel(relation domain.KnowledgeJobRequiredRelation) string {
	if relation.ID != "" {
		return relation.ID
	}
	return fmt.Sprintf("%s -%s-> %s", relation.FromItemID, relation.Kind, relation.ToItemID)
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// normalizeKnowledgeJobRequiredRelation records only visible, concrete graph
// edges. It is called by the relation action after the repository has already
// enforced workspace/requester scope.
func normalizeKnowledgeJobRequiredRelation(relation *domain.KnowledgeRelation) domain.KnowledgeJobRequiredRelation {
	if relation == nil {
		return domain.KnowledgeJobRequiredRelation{}
	}
	return domain.KnowledgeJobRequiredRelation{
		ID: relation.ID, SourceVersionID: relation.SourceVersionID,
		FromItemID: relation.FromItemID, ToItemID: relation.ToItemID,
		Kind: relation.Kind, SourceIDs: append([]string(nil), relation.SourceIDs...),
	}
}

func appendKnowledgeRequiredRelation(job *domain.KnowledgeJob, relation *domain.KnowledgeRelation) {
	if job == nil || relation == nil || relation.ID == "" {
		return
	}
	required := normalizeKnowledgeJobRequiredRelation(relation)
	for i := range job.RequiredRelations {
		if job.RequiredRelations[i].ID == required.ID {
			// Preserve the first identity while unioning evidence observed in a
			// later relation replay.
			job.RequiredRelations[i].SourceIDs = unionKnowledgeStrings(job.RequiredRelations[i].SourceIDs, required.SourceIDs)
			return
		}
	}
	job.RequiredRelations = append(job.RequiredRelations, required)
	sort.Slice(job.RequiredRelations, func(i, j int) bool { return job.RequiredRelations[i].ID < job.RequiredRelations[j].ID })
	job.RequiredItemIDs = appendUniqueString(job.RequiredItemIDs, relation.FromItemID)
	job.RequiredItemIDs = appendUniqueString(job.RequiredItemIDs, relation.ToItemID)
	sort.Strings(job.RequiredItemIDs)
}

func unionKnowledgeStrings(left, right []string) []string {
	out := append([]string(nil), left...)
	for _, value := range right {
		out = appendUniqueString(out, value)
	}
	return out
}

// knowledgeFinishGapText makes explicit exclusions durable. A model may say
// an endpoint is irrelevant, but the control plane still records that reason
// in the incomplete result rather than silently deleting the obligation.
func knowledgeFinishGapText(finish *KnowledgeFinishDecision, gap string) bool {
	if finish == nil {
		return false
	}
	for _, existing := range finish.Gaps {
		if strings.Contains(existing, gap) {
			return true
		}
	}
	return false
}
