package ext

import (
	"sync"
	"time"

	"github.com/danclive/nson-go"
)

type baseToken struct {
	m        sync.RWMutex
	complete chan struct{}
	err      error
}

func (b *baseToken) Wait() bool {
	<-b.complete
	return true
}

func (b *baseToken) WaitTimeout(d time.Duration) bool {
	timer := time.NewTimer(d)
	select {
	case <-b.complete:
		if !timer.Stop() {
			<-timer.C
		}
		return true
	case <-timer.C:
	}

	return false
}

func (b *baseToken) flowComplete() {
	select {
	case <-b.complete:
	default:
		close(b.complete)
	}
}

func (b *baseToken) Error() error {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.err
}

func (b *baseToken) setError(e error) {
	b.m.Lock()
	b.err = e
	b.flowComplete()
	b.m.Unlock()
}

type BaseToken struct {
	baseToken
}

func newBaseToken() *BaseToken {
	return &BaseToken{
		baseToken: baseToken{
			complete: make(chan struct{}),
		},
	}
}

type CallToken struct {
	baseToken
	msg nson.Message
}

func newCallToken() *CallToken {
	return &CallToken{
		baseToken: baseToken{
			complete: make(chan struct{}),
		},
	}
}

func (c *CallToken) setMessage(msg nson.Message) {
	c.m.Lock()
	c.msg = msg
	c.flowComplete()
	c.m.Unlock()
}

func (c *CallToken) Message() nson.Message {
	c.m.Lock()
	defer c.m.Unlock()
	return c.msg
}
