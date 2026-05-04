//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
)

func init() {
	registerPoolSnapshotSignal = registerPoolSnapshotSignalUnix
}

func registerPoolSnapshotSignalUnix(sess share.Session) {
	pool := sess.Pool()
	if pool == nil {
		return
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)
	go func() {
		for {
			select {
			case <-sess.Done():
				signal.Stop(c)
				return
			case <-c:
				fmt.Fprintln(os.Stderr, "ccm share: pool snapshot:")
				for _, line := range pool.SnapshotLines() {
					fmt.Fprintln(os.Stderr, line)
				}
			}
		}
	}()
}
