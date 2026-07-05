package fuzz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

const defaultShutdownTimeout = 10 * time.Second

type managedProcess struct {
	cmd     *exec.Cmd
	done    chan error
	logFile *os.File
}

// ProcessNetwork observes a network and can manage nodes whose NodeSpec
// includes a Command. Nodes without a command are still observable but are not
// startable or stoppable through this type.
type ProcessNetwork struct {
	spec      NetworkSpec
	reader    StatusReader
	mu        sync.Mutex
	processes map[NodeID]*managedProcess
}

func NewProcessNetwork(spec NetworkSpec, reader StatusReader) (*ProcessNetwork, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, ErrNoStatusReader
	}
	return &ProcessNetwork{
		spec:      spec,
		reader:    reader,
		processes: make(map[NodeID]*managedProcess),
	}, nil
}

func (n *ProcessNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *ProcessNetwork) StartNode(ctx context.Context, id NodeID) error {
	node, ok := n.spec.Node(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
	}
	if len(node.Command) == 0 {
		return fmt.Errorf("%w: %s", ErrNodeNotManaged, id)
	}

	n.mu.Lock()
	if existing := n.processes[id]; existing != nil {
		select {
		case err := <-existing.done:
			delete(n.processes, id)
			if err != nil {
				return fmt.Errorf("previous %s process exited: %w", id, err)
			}
		default:
			n.mu.Unlock()
			return nil
		}
	}
	n.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	cmd := exec.Command(node.Command[0], node.Command[1:]...)
	cmd.Dir = node.Dir
	cmd.Env = mergeEnv(os.Environ(), node.Env)

	var logFile *os.File
	if node.LogPath != "" {
		file, err := os.OpenFile(node.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open log file for %s: %w", id, err)
		}
		logFile = file
		cmd.Stdout = file
		cmd.Stderr = file
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return fmt.Errorf("start %s: %w", id, err)
	}

	proc := &managedProcess{
		cmd:     cmd,
		done:    make(chan error, 1),
		logFile: logFile,
	}
	go func() {
		proc.done <- cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	n.mu.Lock()
	n.processes[id] = proc
	n.mu.Unlock()
	return nil
}

func (n *ProcessNetwork) StopNode(ctx context.Context, id NodeID) error {
	node, ok := n.spec.Node(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
	}

	n.mu.Lock()
	proc := n.processes[id]
	n.mu.Unlock()
	if proc == nil {
		if len(node.Command) == 0 {
			return fmt.Errorf("%w: %s", ErrNodeNotManaged, id)
		}
		return nil
	}

	select {
	case err := <-proc.done:
		n.forgetProcess(id)
		if err != nil {
			return fmt.Errorf("%s exited before stop: %w", id, err)
		}
		return nil
	default:
	}

	if proc.cmd.Process != nil {
		_ = proc.cmd.Process.Signal(os.Interrupt)
	}

	timeout := node.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-proc.done:
		n.forgetProcess(id)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if proc.cmd.Process != nil {
			_ = proc.cmd.Process.Kill()
		}
		<-proc.done
		n.forgetProcess(id)
		return nil
	}
}

func (n *ProcessNetwork) RestartNode(ctx context.Context, id NodeID) error {
	if err := n.StopNode(ctx, id); err != nil {
		return err
	}
	return n.StartNode(ctx, id)
}

func (n *ProcessNetwork) Snapshot(ctx context.Context) (Snapshot, error) {
	return snapshot(ctx, n.spec, n.reader)
}

func (n *ProcessNetwork) Close(ctx context.Context) error {
	var errs []error
	for _, id := range n.spec.NodeIDs() {
		if err := n.StopNode(ctx, id); err != nil && !errors.Is(err, ErrNodeNotManaged) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (n *ProcessNetwork) forgetProcess(id NodeID) {
	n.mu.Lock()
	delete(n.processes, id)
	n.mu.Unlock()
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}

	out := append([]string{}, base...)
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, fmt.Sprintf("%s=%s", key, overrides[key]))
	}
	return out
}
