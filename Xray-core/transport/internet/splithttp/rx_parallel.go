package splithttp

import (
	"context"
	"io"
	"math/rand/v2"
	"time"

	"github.com/xtls/xray-core/common/errors"
)

// ponytail: fixed from field data (a healthy segment's headers and 1KiB body
// arrive about 140ms after its connection is ready, frozen connections never
// recover); make it a config field only if a much higher-RTT path needs it.
const (
	rxSegmentResponseTimeout = time.Second
	rxSegmentBodyIdleTimeout = 400 * time.Millisecond
	rxParallelLaunchInterval = 3 * time.Millisecond
)

// rxParallel runs at most N active jobs and keeps at most window/S fixed-size
// jobs, including completed unread jobs.
// All state is guarded by r.mu; network I/O never holds that lock.
type rxParallel struct {
	r                     *rxDownlink
	jobs                  []*rxSegmentJob
	changed               chan struct{}
	stopped               chan struct{}
	next, produced, final uint64
	done, closed          bool
	active                uint64
	nextLaunch            time.Time
}

type rxSegmentJob struct {
	start, end        uint64
	data              []byte
	written, consumed int
	body              *rxHTTPBody
	complete          bool
	finished          bool
	lastProgress      time.Time
	pendingSince      time.Time
	recovery          time.Time
	launchAt          time.Time
}

func newRXParallel(r *rxDownlink, response rxResponse) *rxParallel {
	j := &rxSegmentJob{start: 0, end: response.body.segmentEnd, data: make([]byte, int(r.segment)), body: response.body, lastProgress: time.Now()}
	p := &rxParallel{r: r, jobs: []*rxSegmentJob{j}, changed: make(chan struct{}), stopped: make(chan struct{}), next: j.end, produced: response.end, done: response.done, final: response.end}
	r.current = nil
	return p
}

func (p *rxParallel) signal() { close(p.changed); p.changed = make(chan struct{}) }

func (p *rxParallel) start() {
	p.r.mu.Lock()
	if p.closed {
		p.r.mu.Unlock()
		return
	}
	p.active = 1
	p.peaks()
	go p.run(p.jobs[0], true)
	p.schedule()
	p.r.mu.Unlock()
}

func (p *rxParallel) peaks() {
	s := &p.r.stats
	s.PeakSegmentJobs = max(s.PeakSegmentJobs, uint64(len(p.jobs)))
	s.PeakActiveSegments = max(s.PeakActiveSegments, p.active)
	var buffered uint64
	for _, j := range p.jobs {
		buffered += uint64(j.written - j.consumed)
	}
	s.PeakPrefetchBytes = max(s.PeakPrefetchBytes, buffered)
}

// reserveLaunchLocked 按固定间隔平滑发包，避免并发建连与重连在同一毫秒突发导致限速队列丢包。
func (p *rxParallel) reserveLaunchLocked(minDelay time.Duration, head bool) time.Time {
	now := time.Now().Add(minDelay)
	if p.r.parallel < 8 {
		return now
	}
	if head {
		if now.Add(rxParallelLaunchInterval).After(p.nextLaunch) {
			p.nextLaunch = now.Add(rxParallelLaunchInterval)
		}
		return now
	}
	if p.nextLaunch.After(now) {
		now = p.nextLaunch
	}
	p.nextLaunch = now.Add(rxParallelLaunchInterval)
	return now
}

// schedule keeps at most N GETs active but lets completed jobs queue up to the
// replay window, so one frozen head job does not idle every other connection.
func (p *rxParallel) schedule() {
	for !p.closed && p.r.terminal == nil && p.active < p.r.parallel && uint64(len(p.jobs)) < p.r.window/p.r.segment {
		if (p.done && p.next >= p.final) || p.next > p.produced {
			return
		}
		if len(p.jobs) != 0 && p.jobs[len(p.jobs)-1].end > p.produced {
			return
		}
		if p.next > ^uint64(0)-p.r.segment {
			return
		}
		end := p.next + p.r.segment
		if p.done {
			end = min(end, p.final)
		}
		j := &rxSegmentJob{start: p.next, end: end, data: make([]byte, int(p.r.segment)), lastProgress: time.Now(), launchAt: p.reserveLaunchLocked(0, len(p.jobs) == 0)}
		p.jobs = append(p.jobs, j)
		p.next = end
		p.active++
		p.peaks()
		go p.run(j, false)
	}
}

func (p *rxParallel) observeLocked(response rxResponse) error {
	if response.window != p.r.window {
		return rxFatal("Xray-RX replay window changed")
	}
	if p.done && (response.end > p.final || (response.done && response.end != p.final)) {
		return rxFatal("Xray-RX final offset changed")
	}
	if response.done {
		if response.end < p.produced {
			return rxFatal("Xray-RX final offset precedes produced bytes")
		}
		p.done, p.final = true, response.end
		for _, j := range p.jobs {
			j.end = min(j.end, p.final)
		}
	}
	p.produced = max(p.produced, response.end)
	p.schedule()
	p.signal()
	return nil
}

func (p *rxParallel) observe(response rxResponse) error {
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	return p.observeLocked(response)
}

func (p *rxParallel) run(j *rxSegmentJob, opened bool) {
	r := p.r
	defer func() {
		r.mu.Lock()
		p.active--
		if p.closed && p.active == 0 {
			close(p.stopped)
		}
		j.finished = true
		p.schedule()
		p.signal()
		r.mu.Unlock()
	}()
	var attempt uint
	for {
		r.mu.Lock()
		if p.closed || r.terminal != nil {
			r.mu.Unlock()
			return
		}
		launchAt := j.launchAt
		r.mu.Unlock()
		if !opened {
			if wait := time.Until(launchAt); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-r.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
		r.mu.Lock()
		if p.closed || r.terminal != nil {
			r.mu.Unlock()
			return
		}
		// Retain the unread prefix too. Only consumed bytes advance the ACK,
		// but a replacement GET can start after every byte already buffered.
		start, end := j.start+uint64(j.written), j.end
		if start >= end {
			j.complete = true
			p.signal()
			r.mu.Unlock()
			return
		}
		full := end <= p.produced
		body := j.body
		prevWritten := j.written
		r.mu.Unlock()
		ctx := r.ctx
		var openTimer *time.Timer
		if opened && full {
			openTimer = time.AfterFunc(time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond, func() { body.Close() })
		}
		var err error
		if !opened {
			response, requestErr := r.request(ctx, "segment", start, 1, false, end)
			err = requestErr
			if err == nil {
				body = response.body
				r.mu.Lock()
				if p.closed || r.terminal != nil {
					r.mu.Unlock()
					body.Close()
					return
				}
				j.body, j.lastProgress = body, time.Now()
				err = p.observeLocked(response)
				full = end <= p.produced
				r.mu.Unlock()
				if full {
					openTimer = time.AfterFunc(min(rxSegmentResponseTimeout, time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond), func() { body.Close() })
				}
			}
		}
		opened = false
		if err == nil {
			for {
				r.mu.Lock()
				remaining := int(j.end-j.start) - j.written
				if p.closed || r.terminal != nil {
					r.mu.Unlock()
					err = context.Canceled
					break
				}
				if remaining <= 0 {
					j.complete = true
					p.signal()
					r.mu.Unlock()
					break
				}
				pos := j.written
				r.mu.Unlock()
				n, readErr := body.Read(j.data[pos : pos+remaining])
				r.mu.Lock()
				if n > 0 {
					j.written += n
					j.lastProgress, j.pendingSince = time.Now(), time.Time{}
					p.produced = max(p.produced, j.start+uint64(j.written))
					p.peaks()
					p.schedule()
					p.signal()
				}
				complete := j.start+uint64(j.written) >= j.end
				if complete {
					j.complete = true
					p.signal()
				}
				full = j.end <= p.produced
				r.mu.Unlock()
				if n > 0 && !complete && (full || openTimer != nil) {
					idle := min(rxSegmentBodyIdleTimeout, time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond)
					if openTimer != nil {
						openTimer.Reset(idle)
					} else {
						openTimer = time.AfterFunc(idle, func() { body.Close() })
					}
				}
				if complete {
					break
				}
				if readErr != nil {
					err = readErr
					break
				}
			}
		}
		if openTimer != nil {
			openTimer.Stop()
		}
		if body != nil {
			r.mu.Lock()
			delivered, shutdown := j.complete, p.closed || r.terminal != nil
			r.mu.Unlock()
			if delivered || !shutdown {
				body.report(delivered)
			}
			body.Close()
		}
		r.mu.Lock()
		j.body = nil
		if p.closed || r.terminal != nil {
			r.mu.Unlock()
			return
		}
		if j.complete {
			r.stats.Segments++
			errors.LogInfo(r.ctx, "Xray-RX: parallel segment complete, start ", j.start, ", end ", j.end)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if _, fatal := err.(*rxProtocolError); fatal {
			r.fail(err)
			return
		}
		if err == io.EOF {
			response, _, probeErr := r.probe()
			if _, fatal := probeErr.(*rxProtocolError); fatal {
				r.fail(probeErr)
				return
			}
			if probeErr == nil && response.done {
				r.mu.Lock()
				if j.start+uint64(j.written) == j.end {
					j.complete = true
					p.signal()
					r.mu.Unlock()
					return
				}
				r.mu.Unlock()
			}
		}
		r.mu.Lock()
		if err := r.reserveRetryLocked(false); err != nil {
			r.mu.Unlock()
			r.fail(err)
			return
		}
		if j.recovery.IsZero() {
			j.recovery = time.Now()
		}
		if j.written > prevWritten {
			attempt = 0
		}
		attempt++
		delay := min(2*time.Second, time.Duration(r.opts.RetryDelayMs)*time.Millisecond*time.Duration(1<<min(attempt-1, uint(10))))
		head := len(p.jobs) != 0 && j.start <= p.jobs[0].start+r.segment
		if r.parallel >= 8 {
			capDelay := 200 * time.Millisecond
			if head {
				capDelay = 40 * time.Millisecond
			}
			delay = min(capDelay, delay) + time.Duration(rand.IntN(20))*time.Millisecond
		}
		j.launchAt = p.reserveLaunchLocked(delay, head)
		errors.LogInfo(r.ctx, "Xray-RX: retrying parallel segment, start ", j.start+uint64(j.written), ", end ", j.end, ", attempt ", r.stats.ReconnectAttempts)
		p.signal()
		r.mu.Unlock()
	}
}

func (p *rxParallel) read(dst []byte) (int, error) {
	r := p.r
	r.readMu.Lock()
	defer r.readMu.Unlock()
	for {
		r.mu.Lock()
		if r.terminal != nil {
			err := r.terminal
			r.mu.Unlock()
			return 0, err
		}
		if len(p.jobs) != 0 {
			j := p.jobs[0]
			if j.written > j.consumed {
				n := copy(dst, j.data[j.consumed:j.written])
				j.consumed += n
				r.offset += uint64(n)
				if !j.recovery.IsZero() {
					d := time.Since(j.recovery)
					r.stats.Recoveries++
					if len(r.stats.RecoveryDurations) < 1024 {
						r.stats.RecoveryDurations = append(r.stats.RecoveryDurations, d)
					}
					errors.LogInfo(r.ctx, "Xray-RX: first resumed bytes delivered, offset ", r.offset, ", recovery_ms ", d.Milliseconds(), ", includes_ordered_consumer_wait true")
					j.recovery = time.Time{}
				}
				if r.offset-r.ackOffset >= r.ackThresholdLocked() {
					select {
					case r.ackWake <- struct{}{}:
					default:
					}
				}
				r.mu.Unlock()
				return n, nil
			}
			if j.complete && j.finished {
				p.jobs[0] = nil
				p.jobs = p.jobs[1:]
				p.schedule()
				p.signal()
				r.mu.Unlock()
				continue
			}
		}
		if p.done && r.offset == p.final {
			r.mu.Unlock()
			r.fail(io.EOF)
			return 0, io.EOF
		}
		changed := p.changed
		r.mu.Unlock()
		select {
		case <-r.ctx.Done():
		case <-changed:
		}
	}
}

func (p *rxParallel) detectStall() {
	r := p.r
	if !r.opts.StallDetect {
		return
	}
	var bodies []*rxHTTPBody
	r.mu.Lock()
	for _, j := range p.jobs {
		if j.complete || j.body == nil || j.start+uint64(j.written) >= min(j.end, p.produced) {
			j.pendingSince = time.Time{}
			continue
		}
		if j.pendingSince.IsZero() {
			j.pendingSince = time.Now()
		}
		timeout := time.Duration(r.opts.StallTimeoutMs) * time.Millisecond
		if j.end <= p.produced && j.written > 0 {
			timeout = min(timeout, rxSegmentBodyIdleTimeout)
		}
		if time.Since(j.pendingSince) >= timeout {
			r.stats.ConfirmedStalls++
			bodies = append(bodies, j.body)
			j.pendingSince = time.Time{}
		}
	}
	r.mu.Unlock()
	for _, body := range bodies {
		body.Close()
	}
}

func (p *rxParallel) close() {
	r := p.r
	var bodies []*rxHTTPBody
	r.mu.Lock()
	if !p.closed {
		p.closed = true
		if p.active == 0 {
			close(p.stopped)
		}
		for _, j := range p.jobs {
			if j.body != nil {
				bodies = append(bodies, j.body)
			}
		}
		p.signal()
	}
	r.mu.Unlock()
	for _, body := range bodies {
		body.Close()
	}
}
