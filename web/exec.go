package web

// The real subprocess runners behind the Services seams.

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/procutil"
)

const runTimeout = 30 * time.Second

func runResult(cmd *exec.Cmd) (RunResult, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code, err := procutil.ExitCode(cmd.Run())
	res := RunResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
	return res, err
}

func defaultPrivilegedRun(cfg Config) RunFn {
	return func(args []string) (RunResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		return runResult(exec.CommandContext(ctx, "sudo", append([]string{"-n", cfg.OdioctlBin}, args...)...))
	}
}

func defaultUserRun(args []string) (RunResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	return runResult(exec.CommandContext(ctx, args[0], args[1:]...))
}

// execProcess adapts exec.Cmd to ActionProcess: combined output on one pipe,
// exit reaped by a goroutine so Alive/ExitCode never block.
type execProcess struct {
	cmd  *exec.Cmd
	out  *os.File
	done chan struct{}
	once sync.Once
	code int
}

// defaultSpawn runs a component action as this process's user (the odios
// target user), no sudo: exactly what the operator would type on the box.
func defaultSpawn(argv []string) (ActionProcess, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	_ = w.Close() // the child holds the write end now
	p := &execProcess{cmd: cmd, out: r, done: make(chan struct{})}
	go func() {
		p.code = waitCode(cmd)
		close(p.done)
	}()
	return p, nil
}

// waitCode reaps cmd; -1 when it could not even be waited for.
func waitCode(cmd *exec.Cmd) int {
	code, err := procutil.ExitCode(cmd.Wait())
	if err != nil {
		return -1
	}
	return code
}

func (p *execProcess) Output() io.Reader { return p.out }

func (p *execProcess) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *execProcess) ExitCode() int {
	<-p.done
	return p.code
}

func (p *execProcess) WaitFor(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (p *execProcess) Stop() {
	p.once.Do(func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		if !p.WaitFor(2 * time.Second) {
			_ = p.cmd.Process.Kill()
			p.WaitFor(2 * time.Second)
		}
	})
}
