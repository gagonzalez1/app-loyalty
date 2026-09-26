package cardevents

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const channel = "puntazo_customer_cards"

const (
	maxSubscribers        = 128
	maxSubscribersPerUser = 4
)

var ErrUnavailable = errors.New("card event listener unavailable")
var ErrCapacity = errors.New("card event stream capacity reached")

type subscription struct {
	events chan struct{}
	down   chan struct{}
}

// Broker uses one PostgreSQL LISTEN connection per API process. Notifications
// contain only the affected customer ID; clients receive no customer IDs.
type Broker struct {
	config      *pgx.ConnConfig
	logger      *slog.Logger
	mu          sync.Mutex
	ready       bool
	subscribers map[int64]map[*subscription]struct{}
	total       int
}

func New(config *pgx.ConnConfig, logger *slog.Logger) *Broker {
	return &Broker{config: config.Copy(), logger: logger, subscribers: make(map[int64]map[*subscription]struct{})}
}

func (b *Broker) Subscribe(customerID int64) (<-chan struct{}, <-chan struct{}, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready {
		return nil, nil, nil, ErrUnavailable
	}
	if b.total >= maxSubscribers || len(b.subscribers[customerID]) >= maxSubscribersPerUser {
		return nil, nil, nil, ErrCapacity
	}
	s := &subscription{events: make(chan struct{}, 1), down: make(chan struct{})}
	if b.subscribers[customerID] == nil {
		b.subscribers[customerID] = make(map[*subscription]struct{})
	}
	b.subscribers[customerID][s] = struct{}{}
	b.total++
	return s.events, s.down, func() { b.unsubscribe(customerID, s) }, nil
}

func (b *Broker) unsubscribe(customerID int64, s *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subscribers[customerID][s]; !ok {
		return
	}
	delete(b.subscribers[customerID], s)
	if len(b.subscribers[customerID]) == 0 {
		delete(b.subscribers, customerID)
	}
	b.total--
}

func (b *Broker) publish(customerID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subscribers[customerID] {
		select {
		case s.events <- struct{}{}:
		default: // One pending refresh covers all committed changes.
		}
	}
}

func (b *Broker) setReady() {
	b.mu.Lock()
	b.ready = true
	b.mu.Unlock()
}

func (b *Broker) setDown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ready = false
	for _, group := range b.subscribers {
		for s := range group {
			close(s.down)
		}
	}
	b.subscribers = make(map[int64]map[*subscription]struct{})
	b.total = 0
}

func (b *Broker) Run(ctx context.Context) {
	defer b.setDown()
	for ctx.Err() == nil {
		if err := b.listen(ctx); err != nil && ctx.Err() == nil {
			b.logger.Warn("card event listener disconnected", "error", err)
		}
		b.setDown()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *Broker) listen(ctx context.Context) error {
	conn, err := pgx.ConnectConfig(ctx, b.config)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	b.setReady()
	for {
		// A silent half-open database connection must not leave browser streams
		// looking healthy while card changes are missed. Reconnect periodically;
		// clients compare Last-Event-ID and only reload cards when it changed.
		waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			return err
		}
		if notification == nil || notification.Channel != channel {
			continue
		}
		customerID, err := strconv.ParseInt(notification.Payload, 10, 64)
		if err == nil && customerID > 0 {
			b.publish(customerID)
		}
	}
}
