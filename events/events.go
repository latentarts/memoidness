package events

import "time"

type RuntimeEvent interface {
	EventType() string
}

type Envelope struct {
	ID        string
	Type      string
	Principal string
	Workspace string
	SessionID string
	TurnID    string
	At        time.Time
	Payload   any
}

func (e Envelope) EventType() string {
	return e.Type
}

type Listener func(RuntimeEvent)

type Sink interface {
	Emit(RuntimeEvent) error
}
