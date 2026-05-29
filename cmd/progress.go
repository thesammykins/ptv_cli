package cmd

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type progress struct {
	w        io.Writer
	messages []string
	frames   []string
	delay    time.Duration
	interval time.Duration
	enabled  bool

	done     chan struct{}
	finished chan struct{}
	once     sync.Once
}

var progressMessages = []string{
	"Planning your trip",
	"Checking the platform",
	"Finding the next service",
	"Scanning the network",
	"Plotting the route",
	"Waiting for a green signal",
	"Reading the departure board",
	"Coupling the carriages",
}

var progressFrames = []string{"🚆", "🚉", "🚇", "🚃", "🛤️", "🚦"}

func newProgress() *progress {
	return &progress{
		w:        os.Stderr,
		messages: progressMessages,
		frames:   progressFrames,
		delay:    300 * time.Millisecond,
		interval: 120 * time.Millisecond,
		enabled:  shouldShowProgress(),
	}
}

func shouldShowProgress() bool {
	return !flagJSON && term.IsTerminal(int(os.Stderr.Fd()))
}

func (p *progress) Start() {
	if p == nil || !p.enabled || len(p.messages) == 0 || len(p.frames) == 0 {
		return
	}
	p.done = make(chan struct{})
	p.finished = make(chan struct{})
	message := p.messages[rand.Intn(len(p.messages))]
	frameOffset := rand.Intn(len(p.frames))

	go func() {
		defer close(p.finished)
		timer := time.NewTimer(p.delay)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-p.done:
			return
		}

		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for i := 0; ; i++ {
			p.writeFrame(p.frames[(i+frameOffset)%len(p.frames)], message, i)
			select {
			case <-ticker.C:
			case <-p.done:
				p.clear()
				return
			}
		}
	}()
}

func (p *progress) Stop() {
	if p == nil || !p.enabled || p.done == nil {
		return
	}
	p.once.Do(func() {
		close(p.done)
	})
	<-p.finished
}

func (p *progress) writeFrame(frame, message string, tick int) {
	dots := strings.Repeat(".", tick%4)
	fmt.Fprintf(p.w, "\r%s %s%s", frame, message, dots)
}

func (p *progress) clear() {
	fmt.Fprint(p.w, "\r\033[2K")
}
