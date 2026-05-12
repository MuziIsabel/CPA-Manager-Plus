package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

const defaultSchedulerTickInterval = 30 * time.Second

type SetupResolver func(ctx context.Context) (store.Setup, bool, error)

type SchedulerOptions struct {
	TickInterval time.Duration
	Now          func() time.Time
	Logger       func(format string, args ...any)
}

type SchedulerStatus struct {
	Status         string   `json:"status"`
	Running        bool     `json:"running"`
	StartedAtMS    *int64   `json:"startedAtMs,omitempty"`
	LastTickAtMS   *int64   `json:"lastTickAtMs,omitempty"`
	LastError      string   `json:"lastError,omitempty"`
	RunningTaskIDs []string `json:"runningTaskIds"`
	TickIntervalMS int64    `json:"tickIntervalMs"`
}

type Scheduler struct {
	db      *store.Store
	runner  *Runner
	resolve SetupResolver

	tickInterval time.Duration
	now          func() time.Time
	logf         func(format string, args ...any)

	mu           sync.Mutex
	cancel       context.CancelFunc
	done         chan struct{}
	started      bool
	startedAtMS  *int64
	lastTickAtMS *int64
	lastError    string
	runningTasks map[string]struct{}
}

type scheduleConfig struct {
	Type     string   `json:"type"`
	Every    int      `json:"every"`
	Unit     string   `json:"unit"`
	Times    []string `json:"times"`
	Timezone string   `json:"timezone"`
}

func NewScheduler(db *store.Store, runner *Runner, resolve SetupResolver, options SchedulerOptions) *Scheduler {
	tickInterval := options.TickInterval
	if tickInterval <= 0 {
		tickInterval = defaultSchedulerTickInterval
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	logf := options.Logger
	if logf == nil {
		logf = log.Printf
	}
	if runner == nil {
		runner = NewRunner()
	}
	return &Scheduler{
		db:           db,
		runner:       runner,
		resolve:      resolve,
		tickInterval: tickInterval,
		now:          now,
		logf:         logf,
		lastTickAtMS: nil,
		runningTasks: make(map[string]struct{}),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.db == nil || s.resolve == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	startedAt := s.now().UnixMilli()
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	s.startedAtMS = &startedAt
	s.lastError = ""
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		if _, err := s.db.MarkStaleCodexInspectionRunsInterrupted(runCtx, "usage service scheduler restarted"); err != nil {
			s.recordError(err)
		}
		if _, err := s.runDue(runCtx, true); err != nil {
			s.recordError(err)
		}
		ticker := time.NewTicker(s.tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if _, err := s.runDue(runCtx, true); err != nil {
					s.recordError(err)
				}
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	if cancel != nil {
		cancel()
	}
	s.cancel = nil
	s.started = false
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *Scheduler) Status() SchedulerStatus {
	status := SchedulerStatus{
		Status:         "not_started",
		Running:        false,
		RunningTaskIDs: []string{},
	}
	if s == nil {
		return status
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status.TickIntervalMS = s.tickInterval.Milliseconds()
	status.StartedAtMS = cloneInt64(s.startedAtMS)
	status.LastTickAtMS = cloneInt64(s.lastTickAtMS)
	status.LastError = s.lastError
	status.Running = s.started
	if s.started {
		status.Status = "running"
	} else if s.startedAtMS != nil {
		status.Status = "stopped"
	}
	for id := range s.runningTasks {
		status.RunningTaskIDs = append(status.RunningTaskIDs, id)
	}
	sort.Strings(status.RunningTaskIDs)
	return status
}

func (s *Scheduler) RunDueOnce(ctx context.Context) (int, error) {
	return s.runDue(ctx, false)
}

func (s *Scheduler) runDue(ctx context.Context, async bool) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("scheduler store is required")
	}
	now := s.now()
	nowMS := now.UnixMilli()
	s.setLastTick(nowMS)
	if err := s.reconcileNextRuns(ctx, now); err != nil {
		return 0, err
	}
	tasks, err := s.db.ListDueCodexInspectionTasks(ctx, nowMS)
	if err != nil {
		return 0, err
	}
	launched := 0
	for _, task := range tasks {
		if !s.markTaskRunning(task.ID) {
			continue
		}
		launched++
		if async {
			go func(task store.CodexInspectionTask) {
				defer s.unmarkTaskRunning(task.ID)
				s.executeScheduledTask(ctx, task)
			}(task)
			continue
		}
		s.executeScheduledTask(ctx, task)
		s.unmarkTaskRunning(task.ID)
	}
	return launched, nil
}

func (s *Scheduler) reconcileNextRuns(ctx context.Context, now time.Time) error {
	tasks, err := s.db.ListCodexInspectionTasks(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !task.Enabled {
			if task.NextRunAtMS != nil {
				if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, task.ID, nil); err != nil {
					return err
				}
			}
			continue
		}
		next, err := NextRunAfter(task.Schedule, now)
		if err != nil {
			s.recordError(fmt.Errorf("task %s schedule: %w", task.ID, err))
			continue
		}
		if next == nil {
			if task.NextRunAtMS != nil {
				if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, task.ID, nil); err != nil {
					return err
				}
			}
			continue
		}
		if task.NextRunAtMS == nil {
			nextMS := next.UnixMilli()
			if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, task.ID, &nextMS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) executeScheduledTask(ctx context.Context, task store.CodexInspectionTask) {
	latest, err := s.db.GetCodexInspectionTask(ctx, task.ID)
	if err != nil {
		s.recordError(err)
		return
	}
	if !latest.Enabled {
		if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, latest.ID, nil); err != nil {
			s.recordError(err)
		}
		return
	}
	setup, ok, err := s.resolve(ctx)
	if err != nil {
		s.recordError(err)
		s.scheduleNextAfter(ctx, latest, s.now())
		return
	}
	if !ok {
		s.recordError(errors.New("usage service is not configured"))
		s.scheduleNextAfter(ctx, latest, s.now())
		return
	}
	if _, err := s.runner.Run(ctx, setup, latest, RunOptions{Trigger: "scheduled"}, s.db); err != nil {
		if !errors.Is(err, store.ErrCodexInspectionTaskRunning) {
			s.recordError(err)
		}
	}
	current, err := s.db.GetCodexInspectionTask(ctx, latest.ID)
	if err != nil {
		s.recordError(err)
		return
	}
	s.scheduleNextAfter(ctx, current, s.now())
}

func (s *Scheduler) scheduleNextAfter(ctx context.Context, task store.CodexInspectionTask, after time.Time) {
	if !task.Enabled {
		if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, task.ID, nil); err != nil {
			s.recordError(err)
		}
		return
	}
	next, err := NextRunAfter(task.Schedule, after)
	if err != nil {
		s.recordError(fmt.Errorf("task %s schedule: %w", task.ID, err))
		return
	}
	var nextMS *int64
	if next != nil {
		value := next.UnixMilli()
		nextMS = &value
	}
	if _, err := s.db.UpdateCodexInspectionTaskNextRun(ctx, task.ID, nextMS); err != nil {
		s.recordError(err)
	}
}

func (s *Scheduler) markTaskRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runningTasks[id]; ok {
		return false
	}
	s.runningTasks[id] = struct{}{}
	return true
}

func (s *Scheduler) unmarkTaskRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runningTasks, id)
}

func (s *Scheduler) setLastTick(value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTickAtMS = &value
}

func (s *Scheduler) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	if s.logf != nil {
		s.logf("codex inspection scheduler: %v", err)
	}
}

func NextRunMS(schedule json.RawMessage, after time.Time) (*int64, error) {
	next, err := NextRunAfter(schedule, after)
	if err != nil || next == nil {
		return nil, err
	}
	value := next.UnixMilli()
	return &value, nil
}

func NextRunAfter(raw json.RawMessage, after time.Time) (*time.Time, error) {
	var cfg scheduleConfig
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		cfg.Type = "manual"
	} else if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))
	switch cfg.Type {
	case "", "manual":
		return nil, nil
	case "interval":
		return nextIntervalRun(cfg, after)
	case "daily_times":
		return nextDailyTimeRun(cfg, after)
	default:
		return nil, fmt.Errorf("unsupported schedule type %q", cfg.Type)
	}
}

func nextIntervalRun(cfg scheduleConfig, after time.Time) (*time.Time, error) {
	if cfg.Every <= 0 {
		return nil, errors.New("interval every must be positive")
	}
	var duration time.Duration
	switch strings.ToLower(strings.TrimSpace(cfg.Unit)) {
	case "minute", "minutes", "m":
		duration = time.Duration(cfg.Every) * time.Minute
	case "hour", "hours", "h":
		duration = time.Duration(cfg.Every) * time.Hour
	case "day", "days", "d":
		duration = time.Duration(cfg.Every) * 24 * time.Hour
	default:
		return nil, fmt.Errorf("unsupported interval unit %q", cfg.Unit)
	}
	next := after.Add(duration)
	return &next, nil
}

func nextDailyTimeRun(cfg scheduleConfig, after time.Time) (*time.Time, error) {
	if len(cfg.Times) == 0 {
		return nil, errors.New("daily_times requires at least one time")
	}
	loc, err := scheduleLocation(cfg.Timezone)
	if err != nil {
		return nil, err
	}
	localAfter := after.In(loc)
	points := make([]dailyTimePoint, 0, len(cfg.Times))
	for _, value := range cfg.Times {
		point, err := parseDailyTime(value)
		if err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].seconds < points[j].seconds
	})
	for dayOffset := 0; dayOffset <= 1; dayOffset++ {
		date := localAfter.AddDate(0, 0, dayOffset)
		for _, point := range points {
			candidate := time.Date(
				date.Year(),
				date.Month(),
				date.Day(),
				point.hour,
				point.minute,
				point.second,
				0,
				loc,
			)
			if candidate.After(localAfter) {
				return &candidate, nil
			}
		}
	}
	return nil, errors.New("failed to calculate next daily time")
}

type dailyTimePoint struct {
	hour    int
	minute  int
	second  int
	seconds int
}

func parseDailyTime(value string) (dailyTimePoint, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"15:04", "15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			hour, minute, second := parsed.Clock()
			return dailyTimePoint{
				hour:    hour,
				minute:  minute,
				second:  second,
				seconds: hour*3600 + minute*60 + second,
			}, nil
		}
	}
	return dailyTimePoint{}, fmt.Errorf("invalid daily time %q", value)
}

func scheduleLocation(timezone string) (*time.Location, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	return loc, nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
