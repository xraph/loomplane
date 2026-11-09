package firecracker

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

type TunnelDriver interface {
	DialVM(context.Context, VM) (net.Conn, error)
}
type Publisher struct {
	Host        *Host
	Address     string
	Context     context.Context
	connections chan struct{}
	mu          sync.Mutex
	listeners   map[int]net.Listener
}

func (p *Publisher) Ensure(port int) error {
	if port == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listeners == nil {
		p.listeners = map[int]net.Listener{}
	}
	if p.connections == nil {
		p.connections = make(chan struct{}, 256)
	}
	if _, ok := p.listeners[port]; ok {
		return nil
	}
	listener, err := (&net.ListenConfig{}).Listen(p.Context, "tcp", net.JoinHostPort(p.Address, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	p.listeners[port] = listener
	go p.accept(p.Context, listener, port)
	return nil
}
func (p *Publisher) Run(ctx context.Context) error {
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, l := range p.listeners {
			_ = l.Close()
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := p.reconcile(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func (p *Publisher) reconcile(ctx context.Context) error {
	p.Host.Store.mu.Lock()
	defer p.Host.Store.mu.Unlock()
	records, err := p.Host.Store.List(ctx)
	if err != nil {
		return err
	}
	wanted := map[int]bool{}
	for _, r := range records {
		for _, vm := range []*VM{r.Current, r.Pending} {
			if vm != nil && vm.Spec.PublishedPort > 0 {
				wanted[vm.Spec.PublishedPort] = true
				if err := p.Ensure(vm.Spec.PublishedPort); err != nil {
					return err
				}
			}
		}
	}
	p.mu.Lock()
	for port, l := range p.listeners {
		if !wanted[port] {
			_ = l.Close()
			delete(p.listeners, port)
		}
	}
	p.mu.Unlock()
	return nil
}

func (p *Publisher) accept(ctx context.Context, l net.Listener, port int) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case p.connections <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-p.connections }()
			defer conn.Close()
			records, err := p.Host.Store.List(ctx)
			if err != nil {
				return
			}
			for _, r := range records {
				if !r.Serving() || r.Current.Spec.PublishedPort != port {
					continue
				}
				driver, ok := p.Host.Driver.(TunnelDriver)
				if !ok {
					return
				}
				target, e := driver.DialVM(ctx, *r.Current)
				if e != nil {
					return
				}
				// relay closes both peers before returning.
				relay(conn, target)
				_ = target.Close()
				return
			}
		}()
	}
}
func relay(a, b net.Conn) {
	deadline := time.Now().Add(10 * time.Minute)
	if err := a.SetDeadline(deadline); err != nil {
		return
	}
	if err := b.SetDeadline(deadline); err != nil {
		return
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(a, b); done <- err; _ = a.Close() }()
	_, _ = io.Copy(b, a) //nolint:errcheck // Both peers are closed below regardless of why forwarding ended.
	_ = a.Close()
	_ = b.Close()
	<-done
}
