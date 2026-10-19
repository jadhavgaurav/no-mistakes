package shellenv

import (
	"errors"
	"testing"
	"time"
)

// countScript replays a fixed sequence of observations, then repeats the last
// one, and records how long the caller slept between them.
type countScript struct {
	observations []uint32
	err          error
	errAt        int
	calls        int
	slept        time.Duration
}

func (s *countScript) count() (uint32, error) {
	i := s.calls
	s.calls++
	if s.err != nil && i == s.errAt {
		return s.observations[min(i, len(s.observations)-1)], s.err
	}
	return s.observations[min(i, len(s.observations)-1)], nil
}

func (s *countScript) sleep(d time.Duration) { s.slept += d }

func TestSettleActiveCount(t *testing.T) {
	const window, interval = 100 * time.Millisecond, 25 * time.Millisecond
	queryErr := errors.New("job query failed")

	tests := []struct {
		name       string
		script     countScript
		wantActive uint32
		wantErr    error
		wantCalls  int
		wantSlept  time.Duration
	}{
		{
			name:       "childless exit that is already clean answers at once",
			script:     countScript{observations: []uint32{0}},
			wantActive: 0, wantCalls: 1, wantSlept: 0,
		},
		{
			name:       "console host that lingers briefly is waited out",
			script:     countScript{observations: []uint32{1, 1, 0}},
			wantActive: 0, wantCalls: 3, wantSlept: 2 * interval,
		},
		{
			name:       "settles on the last poll inside the window",
			script:     countScript{observations: []uint32{2, 2, 2, 2, 0}},
			wantActive: 0, wantCalls: 5, wantSlept: window,
		},
		{
			name:       "a child that stays alive is still reported after the bounded wait",
			script:     countScript{observations: []uint32{1}},
			wantActive: 1, wantCalls: 5, wantSlept: window,
		},
		{
			name:       "a count that drains only after the window is not trusted",
			script:     countScript{observations: []uint32{3, 3, 3, 3, 3, 0}},
			wantActive: 3, wantCalls: 5, wantSlept: window,
		},
		{
			name:       "a query error is returned without waiting further",
			script:     countScript{observations: []uint32{1}, err: queryErr, errAt: 1},
			wantActive: 1, wantErr: queryErr, wantCalls: 2, wantSlept: interval,
		},
		{
			name:       "a failing first query is not retried",
			script:     countScript{observations: []uint32{0}, err: queryErr, errAt: 0},
			wantActive: 0, wantErr: queryErr, wantCalls: 1, wantSlept: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := tt.script
			active, err := settleActiveCount(script.count, window, interval, script.sleep)
			if active != tt.wantActive || !errors.Is(err, tt.wantErr) {
				t.Fatalf("settleActiveCount() = (%d, %v), want (%d, %v)", active, err, tt.wantActive, tt.wantErr)
			}
			if script.calls != tt.wantCalls {
				t.Errorf("queried %d times, want %d", script.calls, tt.wantCalls)
			}
			if script.slept != tt.wantSlept {
				t.Errorf("slept %s, want %s", script.slept, tt.wantSlept)
			}
		})
	}
}

func TestSettleActiveCountNeverSleepsPastTheWindow(t *testing.T) {
	// An interval that does not divide the window must clip its last sleep.
	script := countScript{observations: []uint32{1}}
	active, err := settleActiveCount(script.count, 70*time.Millisecond, 25*time.Millisecond, script.sleep)
	if err != nil || active != 1 {
		t.Fatalf("settleActiveCount() = (%d, %v), want (1, nil)", active, err)
	}
	if script.slept != 70*time.Millisecond {
		t.Errorf("slept %s, want exactly the 70ms window", script.slept)
	}
	if script.calls != 4 {
		t.Errorf("queried %d times, want 4", script.calls)
	}
}
