package bus

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type MessageBus struct {
	inbound  chan InboundMessage
	outbound chan OutboundMessage
	handlers map[string]MessageHandler
	mu       sync.RWMutex
}

func NewMessageBus() *MessageBus {
	return &MessageBus{
		inbound:  make(chan InboundMessage, 100),
		outbound: make(chan OutboundMessage, 100),
		handlers: make(map[string]MessageHandler),
	}
}

func (mb *MessageBus) PublishInbound(msg InboundMessage) {
	select {
	case mb.inbound <- msg:
	case <-time.After(10 * time.Second):
		logger.ErrorCF("bus", "PublishInbound timed out, message dropped", map[string]interface{}{
			"channel":   msg.Channel,
			"sender_id": msg.SenderID,
		})
	}
}

func (mb *MessageBus) ConsumeInbound(ctx context.Context) (InboundMessage, bool) {
	select {
	case msg := <-mb.inbound:
		return msg, true
	case <-ctx.Done():
		return InboundMessage{}, false
	}
}

func (mb *MessageBus) PublishOutbound(msg OutboundMessage) {
	select {
	case mb.outbound <- msg:
	case <-time.After(10 * time.Second):
		logger.ErrorCF("bus", "PublishOutbound timed out, message dropped", map[string]interface{}{
			"channel": msg.Channel,
			"chat_id": msg.ChatID,
		})
	}
}

func (mb *MessageBus) SubscribeOutbound(ctx context.Context) (OutboundMessage, bool) {
	select {
	case msg := <-mb.outbound:
		return msg, true
	case <-ctx.Done():
		return OutboundMessage{}, false
	}
}

func (mb *MessageBus) RegisterHandler(channel string, handler MessageHandler) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.handlers[channel] = handler
}

func (mb *MessageBus) GetHandler(channel string) (MessageHandler, bool) {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	handler, ok := mb.handlers[channel]
	return handler, ok
}

// Drain discards remaining messages from both channels before closing.
// Call this during graceful shutdown to unblock any goroutines waiting to send.
func (mb *MessageBus) Drain() {
	for {
		select {
		case <-mb.inbound:
		default:
			goto drainOutbound
		}
	}
drainOutbound:
	for {
		select {
		case <-mb.outbound:
		default:
			return
		}
	}
}

func (mb *MessageBus) Close() {
	mb.Drain()
	close(mb.inbound)
	close(mb.outbound)
}
