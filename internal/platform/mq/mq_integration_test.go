package mq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"constella/internal/platform/events"
)

// These tests need a running broker:
//
//	docker compose -f deploy/docker-compose.yml up -d rabbitmq
//	CONSTELLA_TEST_AMQP=amqp://guest:guest@localhost:5672/ go test ./internal/platform/mq/
func brokerURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("CONSTELLA_TEST_AMQP")
	if url == "" {
		t.Skip("CONSTELLA_TEST_AMQP not set; skipping broker integration test")
	}
	return url
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPublishConsumeAndDeadLetter(t *testing.T) {
	url := brokerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := New(url, "mq-test", quietLog())
	t.Cleanup(func() { _ = c.Close() })

	queue := "test." + events.NewID()[:8]
	key := "test." + events.NewID()[:8] + ".happened"
	t.Cleanup(func() { deleteQueues(t, url, queue, queue+".dlq") })

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
		okIDs    = make(chan string, 4)
	)
	go c.Consume(ctx, Binding{Queue: queue, Keys: []string{key}}, func(_ context.Context, env events.Envelope) error {
		mu.Lock()
		attempts[env.ID]++
		mu.Unlock()
		var data struct{ Fail bool }
		if err := env.Decode(&data); err != nil {
			return err
		}
		if data.Fail {
			return errors.New("poison message")
		}
		okIDs <- env.ID
		return nil
	})
	time.Sleep(time.Second) // let the consumer declare and bind its queue

	good, _ := events.New(key, "test", map[string]bool{"Fail": false})
	bad, _ := events.New(key, "test", map[string]bool{"Fail": true})
	for _, env := range []events.Envelope{good, bad} {
		if err := c.Publish(ctx, env); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	select {
	case id := <-okIDs:
		if id != good.ID {
			t.Fatalf("got %s, want %s", id, good.ID)
		}
	case <-ctx.Done():
		t.Fatal("good message not consumed")
	}

	if n := waitDLQ(ctx, t, url, queue+".dlq", 1); n != 1 {
		t.Fatalf("dlq depth = %d, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts[bad.ID] != 2 {
		t.Fatalf("poison message attempts = %d, want 2 (one retry)", attempts[bad.ID])
	}
}

func waitDLQ(ctx context.Context, t *testing.T, url, dlq string, want int) int {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	for {
		q, err := ch.QueueDeclarePassive(dlq, true, false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.Messages >= want || ctx.Err() != nil {
			return q.Messages
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func deleteQueues(t *testing.T, url string, names ...string) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Log(err)
		return
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Log(err)
		return
	}
	for _, n := range names {
		if _, err := ch.QueueDelete(n, false, false, false); err != nil {
			t.Log(err)
		}
	}
}
