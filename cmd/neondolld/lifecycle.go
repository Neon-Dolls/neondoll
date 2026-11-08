package main

import "sync"

type daemonCleanup struct {
	once    sync.Once
	entries []func() error
	report  func(error)
}

func newDaemonCleanup(report func(error)) *daemonCleanup {
	return &daemonCleanup{report: report}
}

func (c *daemonCleanup) Add(close func() error) {
	if c == nil || close == nil {
		return
	}
	c.entries = append(c.entries, close)
}

func (c *daemonCleanup) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		for index := len(c.entries) - 1; index >= 0; index-- {
			if err := c.entries[index](); err != nil && c.report != nil {
				c.report(err)
			}
		}
	})
}
