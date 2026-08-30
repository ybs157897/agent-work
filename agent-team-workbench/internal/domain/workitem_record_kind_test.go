package domain

import (
	"testing"
	"time"
)

func TestWorkItemRecordKindValidation(t *testing.T) {
	if !RecordKindChat.Valid() || !RecordKindTask.Valid() {
		t.Fatal("chat/task 必须属于 record_kind 闭集")
	}
	if WorkItemRecordKind("other").Valid() {
		t.Fatal("未知 record_kind 不得通过校验")
	}
}

func TestChatWorkItemCannotEnterTaskStateMachine(t *testing.T) {
	chat := &WorkItem{RecordKind: RecordKindChat, Status: WorkItemTodo, Version: 1}
	now := time.Now().UTC()
	if err := chat.Transition(WorkItemInProgress, now); err == nil {
		t.Fatal("Chat 不得进入任务 in_progress 状态")
	}
	if err := chat.EnterReview(now); err == nil {
		t.Fatal("Chat 不得进入任务 review 阶段")
	}
	if err := chat.EnterAcceptance(now); err == nil {
		t.Fatal("Chat 不得进入任务 acceptance 阶段")
	}
	if err := chat.Accept(now); err == nil {
		t.Fatal("Chat 不得通过任务验收")
	}
	chat.BeginExecution(now)
	if chat.Status != WorkItemTodo || chat.Phase != "" || chat.Version != 1 {
		t.Fatalf("Chat 状态机拒绝后不得改变记录：%+v", chat)
	}
}
