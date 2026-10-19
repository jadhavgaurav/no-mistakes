package shellenv

import "time"

// Windows reports a job's ActiveProcesses count straight after the leader
// exits, and that count can still include a process that is already on its way
// out (a console host the leader's job adopted, or accounting that lags the
// exit). The child-completion check therefore polls the count for a short,
// bounded window before it trusts a nonzero value. A process that is really
// still running keeps the count above zero for the whole window and is still
// reported, so the check stays fail-closed.
const (
	activeSettleWindow   = 2 * time.Second
	activeSettleInterval = 25 * time.Millisecond
)

// settleActiveCount polls count until it reports zero, returns an error, or
// the window has been spent sleeping, and returns the last observation. The
// window is measured in sleep calls rather than wall clock so the bound does
// not depend on how long a query takes, and so tests can drive it with a fake
// sleeper.
func settleActiveCount(count func() (uint32, error), window, interval time.Duration, sleep func(time.Duration)) (uint32, error) {
	var waited time.Duration
	for {
		active, err := count()
		if err != nil || active == 0 || waited >= window {
			return active, err
		}
		step := min(interval, window-waited)
		sleep(step)
		waited += step
	}
}
