package viewer

import (
	"context"
	"flag"
	"io"
	"runtime"
	"testing"
	"time"
)

// discardSink accepts everything, so the benchmarks measure the SDK, not a network.
type discardSink struct{}

func (discardSink) Export(context.Context, []Record) error { return nil }

// BenchmarkInfo is the plan6 budget: under 1 µs for a call with four attributes.
func BenchmarkInfo(b *testing.B) {
	log := newLogger(Config{Sink: discardSink{}, Console: ConsoleOff, QueueSize: 1 << 20}, env(nil), time.Now)
	defer log.Close(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200, "currency", "SLE", "ok", true)
	}
}

func BenchmarkInfoWithConsoleJSON(b *testing.B) {
	log := newLogger(Config{Sink: discardSink{}, Console: ConsoleJSON, Output: io.Discard, QueueSize: 1 << 20}, env(nil), time.Now)
	defer log.Close(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info("invoice created", "invoice_id", "inv_123", "amount", 4200, "currency", "SLE", "ok", true)
	}
}

func BenchmarkBelowLevel(b *testing.B) {
	log := newLogger(Config{Sink: discardSink{}, Console: ConsoleOff}, env(nil), time.Now)
	defer log.Close(context.Background())
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Debug("skipped", "k", i)
	}
}

// TestSustainedLoad is the plan6 budget's second half: 10 000 records a second for a
// while, with memory flat. It runs only with -run SustainedLoad -sustained.
func TestSustainedLoad(t *testing.T) {
	if !*sustained {
		t.Skip("run with -sustained")
	}
	log := newLogger(Config{Sink: discardSink{}, Console: ConsoleOff}, env(nil), time.Now)
	defer log.Close(context.Background())
	var ms runtime.MemStats
	heap := func() uint64 { runtime.GC(); runtime.ReadMemStats(&ms); return ms.HeapAlloc }
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	end := time.Now().Add(*duration)
	var first uint64
	for sec := 0; time.Now().Before(end); sec++ {
		for range 100 { // 100 records every 10 ms
			<-tick.C
			for range 100 {
				log.Info("load", "i", sec, "path", "/invoices")
			}
		}
		if sec == 1 {
			first = heap()
		}
	}
	last := heap()
	st := log.Stats()
	t.Logf("sent %d, dropped %d, heap %d KiB → %d KiB", st.Sent, st.QueueFull+st.Failed, first>>10, last>>10)
	if st.QueueFull+st.Failed > 0 || (first > 0 && last > first*2 && last-first > 4<<20) {
		t.Fatalf("not flat: %+v", st)
	}
}

var (
	sustained = flag.Bool("sustained", false, "run TestSustainedLoad")
	duration  = flag.Duration("sustained.for", 60*time.Second, "how long TestSustainedLoad runs")
)
