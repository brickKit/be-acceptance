package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The bus (P12.4, P12.12, P12.13): NATS JetStream, one connection per process named after the
// component, reconnecting for ever; streams and the durable are created when missing and never
// changed.

const (
	ownSubjectPrefix = "conformance.rawstub."
	consumedSubject  = "conformance.owner.updated.v1"
	consumedAggType  = "conformance.peer.owner"
	dlqStream        = "BE_DLQ"
)

// publishedSubjects is contracts/events/rawstub.events.json (and component.yaml publishes).
var publishedSubjects = []string{"conformance.rawstub.archived.v1", "conformance.rawstub.created.v1", "conformance.rawstub.owner_noted.v1"}

// durable is P12.5's name: <component ID with / as _>__<subject with . as __>.
func durable(subject string) string {
	return strings.ReplaceAll(componentID, "/", "_") + "__" + strings.ReplaceAll(subject, ".", "__")
}

func streamName(subject string) string {
	first, _, _ := strings.Cut(subject, ".")
	return "BE_" + strings.ToUpper(first)
}

// Bus is the process's connection.
type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// connectBus connects in the background: a bus that is not up at start is retried (P12.13).
func connectBus(cfg *Config, log *Logger) (*Bus, error) {
	nc, err := nats.Connect(cfg.BusURL, nats.Name(cfg.ComponentID), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second), nats.ReconnectJitter(500*time.Millisecond, 0),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("bus_disconnected", F{"error": errText(err)}) }),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("bus_reconnected", F{}) }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { log.Warn("bus_error", F{"error": errText(err)}) }))
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &Bus{nc: nc, js: js}, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ensure creates the streams of every subject the component publishes or consumes, the dead
// letter stream and the durable, each only when missing (P12.4, P12.5).
func (b *Bus) ensure(ctx context.Context) error {
	streams := map[string]jetstream.StreamConfig{dlqStream: {Name: dlqStream, Subjects: []string{"dlq.>"}, MaxAge: 30 * 24 * time.Hour,
		Storage: jetstream.FileStorage, Replicas: 1, Discard: jetstream.DiscardOld, Duplicates: 10 * time.Minute}}
	for _, s := range append(publishedSubjects, consumedSubject) {
		name := streamName(s)
		first, _, _ := strings.Cut(s, ".")
		streams[name] = jetstream.StreamConfig{Name: name, Subjects: []string{first + ".>"}, MaxAge: 7 * 24 * time.Hour, MaxBytes: 1 << 30,
			Discard: jetstream.DiscardOld, Duplicates: 10 * time.Minute, Storage: jetstream.FileStorage, Replicas: 1}
	}
	for name, cfg := range streams {
		if _, err := b.js.Stream(ctx, name); errors.Is(err, jetstream.ErrStreamNotFound) {
			if _, err := b.js.CreateStream(ctx, cfg); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
				return errors.New("stream " + name + ": " + err.Error())
			}
		} else if err != nil {
			return errors.New("stream " + name + ": " + err.Error())
		}
	}
	name := durable(consumedSubject)
	if _, err := b.js.Consumer(ctx, streamName(consumedSubject), name); errors.Is(err, jetstream.ErrConsumerNotFound) {
		_, err = b.js.CreateConsumer(ctx, streamName(consumedSubject), jetstream.ConsumerConfig{
			Durable: name, FilterSubject: consumedSubject, AckPolicy: jetstream.AckExplicitPolicy, AckWait: 30 * time.Second,
			MaxAckPending: 256, MaxDeliver: -1, DeliverPolicy: jetstream.DeliverAllPolicy, InactiveThreshold: 30 * 24 * time.Hour})
		if err != nil && !errors.Is(err, jetstream.ErrConsumerExists) {
			return errors.New("durable " + name + ": " + err.Error())
		}
	} else if err != nil {
		return errors.New("durable " + name + ": " + err.Error())
	}
	return nil
}
