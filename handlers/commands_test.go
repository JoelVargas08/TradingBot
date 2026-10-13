package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tradingview-bot/models"
)

type fakeSender struct {
	msgs []string
	chat int64
}

func (f *fakeSender) SendMessage(chatID int64, text string) error {
	f.msgs = append(f.msgs, text)
	f.chat = chatID
	return nil
}

func (f *fakeSender) last() string {
	if len(f.msgs) == 0 {
		return ""
	}
	return f.msgs[len(f.msgs)-1]
}

type fakeWorkflow struct {
	available bool
	cancelErr error
	cancelled bool
	runID     string
}

func (f *fakeWorkflow) Available() bool { return f.available }

func (f *fakeWorkflow) CancelCurrent(ctx context.Context) error {
	if f.cancelErr != nil {
		return f.cancelErr
	}
	f.cancelled = true
	return nil
}

func (f *fakeWorkflow) RunID() string { return f.runID }

type fakeSession struct {
	active  bool
	started bool
	stopped bool
}

func (f *fakeSession) Start()                 { f.active = true; f.started = true }
func (f *fakeSession) StartFor(time.Duration) { f.Start() }
func (f *fakeSession) Stop()                  { f.active = false; f.stopped = true }
func (f *fakeSession) IsActive() bool         { return f.active }

func (f *fakeSession) StartedAt() time.Time {
	if !f.active {
		return time.Time{}
	}
	return time.Now()
}

func (f *fakeSession) StatusText() string {
	if f.active {
		return "ACTIVO"
	}
	return "INACTIVO"
}

func (f *fakeSession) ScheduleText() string { return "Sin horario" }
func (f *fakeSession) TimezoneName() string { return "-" }

func newTestHandler(sender MessageSender, session SessionController, workflow WorkflowController) *CommandsHandler {
	return NewCommandsHandler(models.NewUserManager(), sender, nil, session, nil, workflow)
}

func TestHandleSessionReportsWorkflowActive(t *testing.T) {
	sender := &fakeSender{}
	h := newTestHandler(sender, &fakeSession{active: true}, &fakeWorkflow{available: true, runID: "99"})
	h.HandleSession(42)
	if !strings.Contains(sender.last(), "ACTIVO") || !strings.Contains(sender.last(), "99") {
		t.Fatalf("esperaba estado ACTIVO con run id, got %q", sender.last())
	}
}

func TestHandleSessionReportsWorkflowStopped(t *testing.T) {
	sender := &fakeSender{}
	h := newTestHandler(sender, &fakeSession{}, &fakeWorkflow{available: true})
	h.HandleSession(42)
	if !strings.Contains(sender.last(), "DETENIDO") {
		t.Fatalf("esperaba estado DETENIDO, got %q", sender.last())
	}
}

func TestHandleSessionStopCancelsWorkflow(t *testing.T) {
	sender := &fakeSender{}
	workflow := &fakeWorkflow{available: true}
	h := newTestHandler(sender, &fakeSession{active: true}, workflow)
	h.HandleSessionStop(42)
	if !workflow.cancelled {
		t.Fatal("esperaba que se cancelara el workflow PAPER")
	}
	if !strings.Contains(strings.ToLower(sender.last()), "cancelando") {
		t.Fatalf("esperaba confirmación de cancelación, got %q", sender.last())
	}
}

func TestHandleSessionStopReportsCancelError(t *testing.T) {
	sender := &fakeSender{}
	workflow := &fakeWorkflow{available: true, cancelErr: errors.New("HTTP 403")}
	h := newTestHandler(sender, &fakeSession{active: true}, workflow)
	h.HandleSessionStop(42)
	if !strings.Contains(sender.last(), "403") {
		t.Fatalf("esperaba el error de cancelación, got %q", sender.last())
	}
}

func TestHandleSessionStartWhenWorkflowAvailableDoesNotRestart(t *testing.T) {
	sender := &fakeSender{}
	session := &fakeSession{}
	h := newTestHandler(sender, session, &fakeWorkflow{available: true})
	h.HandleSessionStart(42)
	if session.started {
		t.Fatal("no debe reiniciar el gate interno si el runner ya está activo")
	}
	if !strings.Contains(sender.last(), "ya está activo") {
		t.Fatalf("esperaba aviso de workflow ya activo, got %q", sender.last())
	}
}

func TestHandleHelpDocumentsAliases(t *testing.T) {
	sender := &fakeSender{}
	h := newTestHandler(sender, &fakeSession{}, nil)
	h.HandleHelp(42)
	help := sender.last()
	for _, want := range []string{"/stop", "/session_stop", "/session_start", "/start"} {
		if !strings.Contains(help, want) {
			t.Fatalf("la ayuda no documenta %q", want)
		}
	}
}
