// Package s3sink is the funnel-events adapter that writes SearchEvent
// or BookingEvent batches to an S3-compatible bucket (MinIO in dev,
// real S3 in prod). Generic over funnelevents.Event so one file backs
// both search-v1 and booking-v1 sinks. See funnel-sim ADR-0005.
package s3sink

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
)

type Config struct {
	Endpoint   string
	Region     string
	Bucket     string
	AccessKey  string
	SecretKey  string
	UseSSL     bool
	AutoCreate bool
	KeyPrefix  string // e.g. "search-v1/" or "booking-v1/"
	Env        string
	Instance   string
	QueueSize  int
	BatchSize  int
	BatchEvery time.Duration
	Logger     funnelevents.Logger
}

type Sink[T funnelevents.Event] struct {
	cfg     Config
	client  *minio.Client
	metrics funnelevents.Metrics
	queue   chan T
	seq     uint64

	dropped       uint64
	flushed       uint64
	lastDropLogNS int64
	done          chan struct{}
}

const bufferFullLogQuietWindow = 5 * time.Second

func New[T funnelevents.Event](ctx context.Context, cfg Config, metrics funnelevents.Metrics) (*Sink[T], error) {
	cfg = applyDefaults(cfg)
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3sink: bucket is required")
	}
	minio.MaxRetry = 1
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3sink: minio client: %w", err)
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3sink: BucketExists %q: %w", cfg.Bucket, err)
	}
	if !exists {
		if !cfg.AutoCreate {
			return nil, fmt.Errorf("s3sink: bucket %q does not exist (set --auto-create or create it manually)", cfg.Bucket)
		}
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("s3sink: MakeBucket %q: %w", cfg.Bucket, err)
		}
	}
	if metrics == nil {
		metrics = funnelevents.NoopMetrics{}
	}
	return &Sink[T]{
		cfg:     cfg,
		client:  client,
		metrics: metrics,
		queue:   make(chan T, cfg.QueueSize),
		done:    make(chan struct{}),
	}, nil
}

func (s *Sink[T]) Start(ctx context.Context) { go s.run(ctx) }
func (s *Sink[T]) Done() <-chan struct{}     { return s.done }

func (s *Sink[T]) Publish(e T) {
	select {
	case s.queue <- e:
	default:
		s.recordDrop("buffer_full", 1)
		s.maybeLogBufferFullDrop()
	}
}

func (s *Sink[T]) maybeLogBufferFullDrop() {
	if s.cfg.Logger == nil {
		return
	}
	now := time.Now().UnixNano()
	prev := atomic.LoadInt64(&s.lastDropLogNS)
	if now-prev < int64(bufferFullLogQuietWindow) {
		return
	}
	if !atomic.CompareAndSwapInt64(&s.lastDropLogNS, prev, now) {
		return
	}
	s.cfg.Logger.Warn("funnel.event.sink.buffer_full", map[string]any{
		"queue_capacity":   s.cfg.QueueSize,
		"lifetime_dropped": atomic.LoadUint64(&s.dropped),
		"key_prefix":       s.cfg.KeyPrefix,
	})
}

func (s *Sink[T]) run(ctx context.Context) {
	defer close(s.done)
	// batches keyed by Hive partition segment ("" or "event_type=…")
	batches := map[string][]T{}
	batchBytes := 0
	timer := time.NewTimer(s.cfg.BatchEvery)
	defer timer.Stop()

	flushAll := func(uploadCtx context.Context) {
		for partition, batch := range batches {
			if len(batch) == 0 {
				continue
			}
			payload, err := serializeBatch(batch)
			if err != nil {
				s.recordDrop("serialize_failed", len(batch))
				continue
			}
			if err := s.upload(uploadCtx, partition, payload); err != nil {
				s.recordDrop("flush_failed", len(batch))
				s.log("funnel.event.sink.flush_failed", map[string]any{
					"events":     len(batch),
					"bytes":      len(payload),
					"partition":  partition,
					"key_prefix": s.cfg.KeyPrefix,
					"error":      err.Error(),
				})
				continue
			}
			atomic.AddUint64(&s.flushed, uint64(len(batch)))
			s.metrics.IncFlushed(len(batch), len(payload))
			s.metrics.IncObject()
			s.log("funnel.event.sink.flushed", map[string]any{
				"events":     len(batch),
				"bytes":      len(payload),
				"partition":  partition,
				"key_prefix": s.cfg.KeyPrefix,
			})
		}
		batches = map[string][]T{}
		batchBytes = 0
	}

	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.cfg.BatchEvery)
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case e := <-s.queue:
					partition := e.Partition()
					batches[partition] = append(batches[partition], e)
					batchBytes += estimateSize(e)
				default:
					goto drained
				}
			}
		drained:
			shutdownCtx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
			flushAll(shutdownCtx)
			scancel()
			return
		case e := <-s.queue:
			partition := e.Partition()
			batches[partition] = append(batches[partition], e)
			batchBytes += estimateSize(e)
			if batchBytes >= s.cfg.BatchSize {
				flushAll(ctx)
				resetTimer()
			}
		case <-timer.C:
			flushAll(ctx)
			timer.Reset(s.cfg.BatchEvery)
		}
	}
}

func (s *Sink[T]) upload(ctx context.Context, partition string, payload []byte) error {
	seq := atomic.AddUint64(&s.seq, 1)
	key := s.objectKey(time.Now().UTC(), partition, seq)
	backoffs := []time.Duration{100 * time.Millisecond, 400 * time.Millisecond, 1600 * time.Millisecond}
	var lastErr error
	for i, wait := range backoffs {
		_, err := s.client.PutObject(ctx, s.cfg.Bucket, key, bytes.NewReader(payload), int64(len(payload)),
			minio.PutObjectOptions{ContentType: "application/x-gzip", ContentEncoding: "gzip"})
		if err == nil {
			return nil
		}
		lastErr = err
		if i < len(backoffs)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return lastErr
}

func (s *Sink[T]) objectKey(now time.Time, partition string, seq uint64) string {
	partitionSeg := ""
	if partition != "" {
		partitionSeg = partition + "/"
	}
	return fmt.Sprintf("%sdt=%s/hour=%02d/env=%s/%sinstance=%s/batch-%s-%06d.jsonl.gz",
		s.cfg.KeyPrefix,
		now.Format("2006-01-02"),
		now.Hour(),
		s.cfg.Env,
		partitionSeg,
		s.cfg.Instance,
		now.Format("20060102T150405Z"),
		seq,
	)
}

func serializeBatch[T funnelevents.Event](events []T) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := json.NewEncoder(gz)
	for i := range events {
		if err := enc.Encode(events[i]); err != nil {
			_ = gz.Close()
			return nil, err
		}
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// estimateSize is a fast, coarse per-event byte estimate used only to
// trigger the BatchSize flush. Real byte count is measured post-gzip
// at flush time and fed to the flushed_bytes metric.
func estimateSize[T funnelevents.Event](e T) int {
	// Coarse estimate: JSON marshal length capped by a heuristic. This
	// runs on every Publish so it must stay allocation-light.
	b, err := json.Marshal(e)
	if err != nil {
		return 512
	}
	return len(b)
}

func (s *Sink[T]) recordDrop(reason string, n int) {
	atomic.AddUint64(&s.dropped, uint64(n))
	s.metrics.IncDropped(reason, n)
}

func (s *Sink[T]) log(msg string, attrs map[string]any) {
	if s.cfg.Logger == nil {
		return
	}
	s.cfg.Logger.Info(msg, attrs)
}

func (s *Sink[T]) Dropped() uint64 { return atomic.LoadUint64(&s.dropped) }
func (s *Sink[T]) Flushed() uint64 { return atomic.LoadUint64(&s.flushed) }

func applyDefaults(c Config) Config {
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.QueueSize == 0 {
		c.QueueSize = 10000
	}
	if c.BatchSize == 0 {
		c.BatchSize = 10 * 1024 * 1024
	}
	if c.BatchEvery == 0 {
		c.BatchEvery = 5 * time.Minute
	}
	return c
}
