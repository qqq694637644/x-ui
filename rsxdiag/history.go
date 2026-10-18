package rsxdiag

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Points are aggregates of genuine observations, not scheduled time. Missing
// samples stay null. Minutes while the panel was stopped are unknown, not up.
type Point struct {
	Time       time.Time           `json:"time"`
	Seconds    float64             `json:"observed_seconds"`
	Values     map[string]*float64 `json:"values"`
	Totals     map[string]float64  `json:"totals"`
	Config     map[string]string   `json:"config,omitempty"`
	Histograms map[string][]uint64 `json:"histograms,omitempty"`
}
type minuteAccumulator struct {
	time       time.Time
	seconds    float64
	sums       map[string]float64
	weights    map[string]float64
	totals     map[string]float64
	config     map[string]string
	histograms map[string][]uint64
}

func newAccumulator(at time.Time) *minuteAccumulator {
	return &minuteAccumulator{time: at, sums: map[string]float64{}, weights: map[string]float64{}, totals: map[string]float64{}}
}
func (a *minuteAccumulator) add(p Point) {
	if a.histograms == nil {
		a.histograms = map[string][]uint64{}
	}
	for key, h := range p.Histograms {
		if a.histograms[key] == nil {
			a.histograms[key] = NewHistogram()
		}
		MergeHistogram(a.histograms[key], h)
	}
	a.seconds += p.Seconds
	for k, v := range p.Values {
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			a.sums[k] += *v * p.Seconds
			a.weights[k] += p.Seconds
		}
	}
	for k, v := range p.Totals {
		a.totals[k] += v
	}
	a.config = p.Config
}
func (a *minuteAccumulator) point() Point {
	p := Point{Time: a.time, Seconds: a.seconds, Values: map[string]*float64{}, Totals: map[string]float64{}, Config: map[string]string{}, Histograms: map[string][]uint64{}}
	for k, v := range a.totals {
		p.Totals[k] = v
	}
	for k, v := range a.config {
		p.Config[k] = v
	}
	for k, v := range a.histograms {
		p.Histograms[k] = append([]uint64(nil), v...)
	}
	for k, sum := range a.sums {
		v := sum / a.weights[k]
		p.Values[k] = &v
	}
	return p
}

type History struct {
	mu         sync.Mutex
	dir        string
	recent     []Point
	minute     *minuteAccumulator
	queue      chan Point
	done       chan struct{}
	stop       chan struct{}
	once       sync.Once
	lastErr    string
	dropped    uint64
	retainDays int
	budget     int64
}

func NewHistory(dir string, retentionDays int, budget int64) (*History, error) {
	if retentionDays < 1 || retentionDays > 30 {
		return nil, errors.New("retention must be 1..30 days")
	}
	if budget < 1<<20 || budget > 512<<20 {
		return nil, errors.New("history budget must be 1..512 MiB")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	h := &History{dir: dir, queue: make(chan Point, 128), done: make(chan struct{}), stop: make(chan struct{}), retainDays: retentionDays, budget: budget}
	go h.writer()
	return h, nil
}
func privateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("diagnostic directory must not be a symlink")
	}
	return os.Chmod(dir, 0700)
}
func (h *History) Add(p Point) {
	if p.Seconds <= 0 || p.Seconds > 15 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recent = append(h.recent, p)
	cut := p.Time.Add(-time.Hour)
	i := 0
	for i < len(h.recent) && (h.recent[i].Time.Before(cut) || len(h.recent)-i > 720) {
		i++
	}
	if i > 0 {
		h.recent = append([]Point(nil), h.recent[i:]...)
	}
	minute := p.Time.UTC().Truncate(time.Minute)
	if h.minute == nil {
		h.minute = newAccumulator(minute)
	}
	if minute.After(h.minute.time) {
		if h.minute.seconds > 0 {
			select {
			case h.queue <- h.minute.point():
			default:
				h.dropped++
				h.lastErr = "history writer busy; minute lost"
			}
		}
		h.minute = newAccumulator(minute)
	}
	h.minute.add(p)
}
func (h *History) Recent(since time.Time) []Point {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := []Point{}
	for _, p := range h.recent {
		if !p.Time.Before(since) {
			v = append(v, p)
		}
	}
	return v
}
func (h *History) Status() map[string]interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]interface{}{"error": h.lastErr, "dropped_minutes": h.dropped, "retention_days": h.retainDays, "budget_bytes": h.budget}
}
func (h *History) SetLimits(days int, budget int64) {
	h.mu.Lock()
	h.retainDays = days
	h.budget = budget
	h.mu.Unlock()
}
func (h *History) Close() {
	h.once.Do(func() {
		h.mu.Lock()
		if h.minute != nil && h.minute.seconds > 0 {
			select {
			case h.queue <- h.minute.point():
			default:
				h.dropped++
			}
			h.minute = nil
		}
		h.mu.Unlock()
		close(h.stop)
		<-h.done
	})
}
func (h *History) writer() {
	defer close(h.done)
	write := func(p Point) {
		if err := h.append(p); err != nil {
			h.mu.Lock()
			h.lastErr = err.Error()
			h.mu.Unlock()
		}
	}
	for {
		select {
		case p := <-h.queue:
			write(p)
		case <-h.stop:
			for {
				select {
				case p := <-h.queue:
					write(p)
				default:
					return
				}
			}
		}
	}
}
func (h *History) append(p Point) error {
	path := filepath.Join(h.dir, p.Time.UTC().Format("2006-01-02")+".jsonl")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe history file")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(raw) > 32768 {
		return errors.New("minute sample exceeds size budget")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return h.prune(time.Now().UTC())
}
func (h *History) prune(now time.Time) error {
	h.mu.Lock()
	days, budget := h.retainDays, h.budget
	h.mu.Unlock()
	files, err := os.ReadDir(h.dir)
	if err != nil {
		return err
	}
	type entry struct {
		path string
		size int64
	}
	kept := []entry{}
	var total int64
	cut := now.AddDate(0, 0, -days).Format("2006-01-02")
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(f.Name(), ".jsonl")
		if _, err := time.Parse("2006-01-02", day); err != nil {
			continue
		}
		info, err := f.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(h.dir, f.Name())
		if day < cut {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		kept = append(kept, entry{path, info.Size()})
		total += info.Size()
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].path < kept[j].path })
	for _, f := range kept {
		if total <= budget {
			break
		}
		if err := os.Remove(f.path); err != nil {
			return err
		}
		total -= f.size
	}
	return nil
}
func (h *History) Read(since time.Time, maxPoints int) ([]Point, error) {
	h.mu.Lock()
	budget := h.budget
	h.mu.Unlock()
	if time.Since(since) <= time.Hour {
		return h.Recent(since), nil
	}
	if maxPoints < 1 || maxPoints > 2000 {
		maxPoints = 1000
	}
	files, err := os.ReadDir(h.dir)
	if err != nil {
		return nil, err
	}
	out := []Point{}
	var readBytes int64
	for _, entry := range files {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		day := strings.TrimSuffix(entry.Name(), ".jsonl")
		if _, err := time.Parse("2006-01-02", day); err != nil || day < since.UTC().Format("2006-01-02") {
			continue
		}
		readBytes += info.Size()
		if readBytes > budget+(1<<20) {
			return nil, errors.New("history exceeds read budget")
		}
		f, err := os.Open(filepath.Join(h.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 32768), 65536)
		for scanner.Scan() {
			var p Point
			if json.Unmarshal(scanner.Bytes(), &p) == nil && !p.Time.Before(since) {
				out = append(out, p)
			}
		}
		scanErr := scanner.Err()
		_ = f.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	h.mu.Lock()
	if h.minute != nil && h.minute.seconds > 0 {
		out = append(out, h.minute.point())
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) <= maxPoints {
		return out, nil
	}
	step := (len(out) + maxPoints - 1) / maxPoints
	result := []Point{}
	for i := 0; i < len(out); i += step {
		a := newAccumulator(out[i].Time)
		end := i + step
		if end > len(out) {
			end = len(out)
		}
		for _, p := range out[i:end] {
			a.add(p)
		}
		result = append(result, a.point())
	}
	return result, nil
}

type Stability struct {
	Distributions   map[string]interface{} `json:"distributions"`
	ObservedSeconds float64                `json:"observed_seconds"`
	ExpectedSeconds float64                `json:"expected_seconds"`
	CoveragePct     float64                `json:"coverage_pct"`
	Availability    map[string]*float64    `json:"availability_pct"`
	Totals          map[string]float64     `json:"totals"`
	Note            string                 `json:"note"`
}

func Summarize(points []Point, span time.Duration) Stability {
	s := Stability{ExpectedSeconds: span.Seconds(), Availability: map[string]*float64{}, Totals: map[string]float64{}, Note: "Availability covers observed seconds only; gaps and stopped monitoring are not counted as uptime. Rates and quantiles are not global end-to-end delivery guarantees."}
	dur := map[string]float64{}
	histograms := map[string][]uint64{}
	sum := map[string]float64{}
	for _, p := range points {
		for k, h := range p.Histograms {
			if histograms[k] == nil {
				histograms[k] = NewHistogram()
			}
			MergeHistogram(histograms[k], h)
		}
		s.ObservedSeconds += p.Seconds
		for k, v := range p.Values {
			if strings.HasSuffix(k, "_online") && v != nil {
				dur[k] += p.Seconds
				sum[k] += *v * p.Seconds
			}
		}
		for k, v := range p.Totals {
			s.Totals[k] += v
		}
	}
	for k, v := range sum {
		n := v / dur[k] * 100
		s.Availability[k] = &n
	}
	if s.ExpectedSeconds > 0 {
		s.CoveragePct = math.Min(100, s.ObservedSeconds/s.ExpectedSeconds*100)
	}
	s.Distributions = map[string]interface{}{}
	for k, h := range histograms {
		s.Distributions[k] = HistogramQuantiles(h)
	}
	return s
}
