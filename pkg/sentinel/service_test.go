package sentinel

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/state"
)

func TestSendAlert_ThrottleByType(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()
	stateMgr := state.NewManager(tmpDir)
	if err := stateMgr.SetLastChannel("telegram:123"); err != nil {
		t.Fatalf("set last channel: %v", err)
	}

	svc := NewService(Config{Enabled: true, Workspace: tmpDir}, stateMgr)
	svc.lastAlertTime = map[string]time.Time{}
	msgBus := bus.NewMessageBus()
	svc.SetBus(msgBus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.sendAlert("cpu_temp", "CPU temperatura alta: 80.1°C")
	expectOutbound(t, msgBus, ctx, "⚠️ CPU temperatura alta: 80.1°C")

	// A slightly different temperature should still be throttled because it is the same alert type.
	svc.sendAlert("cpu_temp", "CPU temperatura alta: 80.4°C")
	expectNoOutbound(t, msgBus, ctx)

	svc.sendAlert("ram", "RAM crítica: 91.0% usada")
	expectOutbound(t, msgBus, ctx, "⚠️ RAM crítica: 91.0% usada")
}

func expectOutbound(t *testing.T, msgBus *bus.MessageBus, parent context.Context, want string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, 200*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("expected outbound message")
	default:
	}

	msg, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound message")
	}
	if msg.Content != want {
		t.Fatalf("unexpected content: got %q want %q", msg.Content, want)
	}
}

func expectNoOutbound(t *testing.T, msgBus *bus.MessageBus, parent context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, 150*time.Millisecond)
	defer cancel()

	msg, ok := msgBus.SubscribeOutbound(ctx)
	if ok {
		t.Fatalf("unexpected outbound message: %+v", msg)
	}
}
