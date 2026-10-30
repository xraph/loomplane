//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/mdlayher/vsock"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/firecracker/guest"
)

type hostListener struct{ net.Listener }

func (l hostListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		addr, ok := c.RemoteAddr().(*vsock.Addr)
		if ok && addr.ContextID == vsock.Host {
			return c, nil
		}
		_ = c.Close()
	}
}
func main() {
	listener, err := vsock.Listen(fc.GuestPort, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	service := &guest.Service{Root: "/run/loomplane"}
	go func() {
		if e := service.RunDNS(context.Background()); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
	}()
	server := &http.Server{Handler: service, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 31 * time.Minute, WriteTimeout: 31 * time.Minute, MaxHeaderBytes: 64 << 10}
	if err = server.Serve(hostListener{listener}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
