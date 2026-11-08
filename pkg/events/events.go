package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	Stream = "ATLAS"

	UserCreated             = "atlas.user.created"
	DocumentIngested        = "atlas.knowledge.document.ingested"
	ReminderDue             = "atlas.planner.reminder.due"
	NotificationRequested   = "atlas.notification.requested"
	NotificationDelivered   = "atlas.notification.delivered"
)

type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Producer   string          `json:"producer"`
	Payload    json.RawMessage `json:"payload"`
}

type Bus struct {
	nc       *nats.Conn
	js       nats.JetStreamContext
	producer string
}

func Connect(ctx context.Context, url, producer string) (*Bus, error) {
	var nc *nats.Conn
	var err error
	for i := 0; i < 30; i++ {
		nc, err = nats.Connect(url, nats.Name(producer), nats.Timeout(3*time.Second))
		if err == nil {
			break
		}
		slog.Warn("waiting for nats", "attempt", i+1, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      Stream,
		Subjects:  []string{"atlas.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		MaxAge:    7 * 24 * time.Hour,
	})
	if err != nil && err != nats.ErrStreamNameAlreadyInUse {
		// AddStream returns the stream if it exists on some versions; ignore already exists.
		if !isStreamExists(err) {
			nc.Close()
			return nil, fmt.Errorf("add stream: %w", err)
		}
	}
	return &Bus{nc: nc, js: js, producer: producer}, nil
}

func isStreamExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == nats.ErrStreamNameAlreadyInUse.Error() ||
		strings.Contains(msg, "stream name already in use") ||
		strings.Contains(msg, "already exists")
}

func (b *Bus) Publish(ctx context.Context, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{
		ID:         uuid.NewString(),
		Type:       eventType,
		OccurredAt: time.Now().UTC(),
		Producer:   b.producer,
		Payload:    body,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(eventType, data, nats.Context(ctx))
	return err
}

func (b *Bus) Subscribe(durable, subject string, handler func(Envelope) error) (*nats.Subscription, error) {
	return b.QueueSubscribe(durable, durable, subject, handler)
}

func (b *Bus) QueueSubscribe(durable, queue, subject string, handler func(Envelope) error) (*nats.Subscription, error) {
	return b.js.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		var env Envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			slog.Error("bad event payload", "err", err)
			_ = msg.Ack()
			return
		}
		if err := handler(env); err != nil {
			slog.Error("event handler failed", "type", env.Type, "err", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}, nats.Durable(durable), nats.ManualAck(), nats.AckWait(30*time.Second), nats.DeliverNew())
}

func (b *Bus) Close() {
	if b != nil && b.nc != nil {
		b.nc.Drain()
	}
}

func Decode[T any](env Envelope) (T, error) {
	var v T
	err := json.Unmarshal(env.Payload, &v)
	return v, err
}

type UserCreatedPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

type DocumentIngestedPayload struct {
	UserID     string `json:"user_id"`
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
}

type ReminderDuePayload struct {
	UserID      string `json:"user_id"`
	ReminderID  string `json:"reminder_id"`
	Title       string `json:"title"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	Body        string `json:"body"`
}

type NotificationRequestedPayload struct {
	UserID      string `json:"user_id"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
}
