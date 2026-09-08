package kimiapp

import (
	"context"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

func backgroundFrame(kind, task, agent string, detached bool) wsFrame {
	return wsFrame{Type: kind, Payload: mustJSON(map[string]any{
		"agentId": agent,
		"info":    map[string]any{"taskId": task, "status": "running", "detached": detached},
	})}
}

func newBackgroundPump() *eventPump {
	return &eventPump{
		ex:    newTestExec(context.Background(), "", newRecordCallbacks(), make(chan runtime.Control, 1)),
		state: &turnState{activeTurn: 1, activeSeen: true, endReason: "completed", promptID: "p_1"},
	}
}

func TestBackgroundLifecycleIgnoresSynchronousForeignAndUnknownTasks(t *testing.T) {
	p := newBackgroundPump()
	for _, frame := range []wsFrame{
		backgroundFrame("task.started", "sync", "main", false),
		backgroundFrame("task.terminated", "sync", "main", false),
		backgroundFrame("task.started", "child", "worker", true),
		backgroundFrame("task.terminated", "child", "worker", true),
		backgroundFrame("task.terminated", "unknown", "main", true),
	} {
		p.handle(frame)
	}
	if p.holdForBackgroundTurn() {
		t.Fatal("unrelated or synchronous tasks must not keep a completed Run open")
	}
}

func TestBackgroundLifecycleKeepsNotificationAfterEarlyTerminationAndDeduplicatesAliases(t *testing.T) {
	p := newBackgroundPump()
	p.handle(backgroundFrame("task.started", "bg", "main", true))
	p.handle(backgroundFrame("background.task.started", "bg", "main", true))
	if len(p.state.pendingBackgroundTasks) != 1 {
		t.Fatal("native and alias starts must identify the same task")
	}
	p.handle(backgroundFrame("task.terminated", "bg", "main", true))
	p.handle(backgroundFrame("background.task.terminated", "bg", "main", true))
	if !p.holdForBackgroundTurn() || len(p.state.pendingBackgroundNotifications) != 1 {
		t.Fatal("task completion must retain its pending notification even before main turn end")
	}
	p.state.beginBackgroundNotificationTurn("bg")
	p.handle(backgroundFrame("background.task.terminated", "bg", "main", true))
	if len(p.state.pendingBackgroundNotifications) != 0 || len(p.state.pendingBackgroundTasks) != 0 {
		t.Fatal("a duplicate terminal alias must not resurrect a consumed notification")
	}
}

func TestBackgroundLifecycleDoesNotHoldFailedOrCancelledTurns(t *testing.T) {
	for _, reason := range []string{"failed", "cancelled", "aborted"} {
		p := newBackgroundPump()
		p.handle(backgroundFrame("task.started", "bg", "main", true))
		p.state.endReason = reason
		if p.holdForBackgroundTurn() {
			t.Fatalf("must not hold terminal reason %s", reason)
		}
	}
	p := newBackgroundPump()
	p.handle(backgroundFrame("task.started", "bg", "main", true))
	p.state.failure = &runtime.Failure{Code: "provider_error"}
	if p.holdForBackgroundTurn() {
		t.Fatal("an authoritative failure must not be hidden by background work")
	}
}

func TestBackgroundReplayBatchConsumesNotificationTurnAfterFirstTurnEnd(t *testing.T) {
	p := newBackgroundPump()
	frame := func(kind string, payload map[string]any) wsFrame {
		payload["agentId"] = "main"
		return wsFrame{Type: kind, Payload: mustJSON(payload)}
	}
	stream := &wsStream{pending: []wsFrame{
		backgroundFrame("task.started", "bg", "main", true),
		frame("turn.ended", map[string]any{"turnId": 1, "reason": "completed"}),
		backgroundFrame("task.terminated", "bg", "main", true),
		frame("turn.started", map[string]any{"turnId": 2, "origin": map[string]any{"kind": "task", "taskId": "bg", "status": "completed", "notificationId": "task:bg:completed"}}),
		frame("task.notified", map[string]any{"sourceKind": "background_task", "sourceId": "bg"}),
		frame("assistant.delta", map[string]any{"turnId": 2, "delta": "知识查询结果"}),
		frame("turn.ended", map[string]any{"turnId": 2, "reason": "completed"}),
	}}
	if !p.drain(stream) {
		t.Fatal("the entire replay batch must reach the notification turn's final outcome")
	}
	if p.state.answer.String() != "知识查询结果" || p.state.activeTurn != 2 {
		t.Fatalf("frames after the first turn end were discarded: %+v", p.state)
	}
}

func TestBackgroundWaitDeliveredConsumesResultWithoutExpectingAnotherTurn(t *testing.T) {
	p := newBackgroundPump()
	p.handle(backgroundFrame("task.started", "bg", "main", true))
	p.handle(backgroundFrame("task.terminated", "bg", "main", true))
	p.handle(wsFrame{Type: "task.waitDelivered", Payload: mustJSON(map[string]any{
		"agentId": "main", "keys": []string{"bg\x00completed\x00task:bg:completed"},
	})})
	if p.holdForBackgroundTurn() {
		t.Fatal("WaitFor already delivered the background result; main completion must not wait for a suppressed notification")
	}
}

func TestBackgroundWaitDeliveredDoesNotConsumeAnotherTaskOrAgent(t *testing.T) {
	p := newBackgroundPump()
	p.handle(backgroundFrame("task.started", "bg", "main", true))
	p.handle(backgroundFrame("task.terminated", "bg", "main", true))
	for _, payload := range []map[string]any{
		{"agentId": "worker", "keys": []string{"bg\x00completed\x00task:bg:completed"}},
		{"agentId": "main", "keys": []string{"other\x00completed\x00task:other:completed"}},
		{"agentId": "main", "keys": []string{"bg"}},
	} {
		p.handle(wsFrame{Type: "task.waitDelivered", Payload: mustJSON(payload)})
	}
	if !p.holdForBackgroundTurn() {
		t.Fatal("unrelated or malformed delivery markers must not discard the Run's pending result")
	}
}
