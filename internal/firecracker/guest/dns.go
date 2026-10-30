package guest

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

func (s *Service) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	reply := new(dns.Msg)
	reply.SetReply(r)
	if len(r.Question) != 1 {
		reply.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(reply) //nolint:errcheck // A disconnected DNS client cannot receive a second error.
		return
	}
	question := r.Question[0]
	name := strings.TrimSuffix(strings.ToLower(question.Name), ".")
	s.networkMu.RLock()
	network := s.network
	s.networkMu.RUnlock()
	if strings.HasSuffix(name, ".loomplane") {
		ip := net.ParseIP(network.Peers[name])
		if ip == nil {
			reply.Rcode = dns.RcodeNameError
		} else if question.Qtype == dns.TypeA {
			reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 2}, A: ip.To4()}}
		}
		_ = w.WriteMsg(reply) //nolint:errcheck // A disconnected DNS client cannot receive a second error.
		return
	}
	client := &dns.Client{Timeout: 2 * time.Second}
	for _, server := range network.DNS {
		out, _, err := client.Exchange(r, net.JoinHostPort(server, "53"))
		if err == nil {
			_ = w.WriteMsg(out) //nolint:errcheck // A disconnected DNS client cannot receive a second error.
			return
		}
	}
	reply.Rcode = dns.RcodeServerFailure
	_ = w.WriteMsg(reply) //nolint:errcheck // A disconnected DNS client cannot receive a second error.
}
func (s *Service) RunDNS(ctx context.Context) error {
	udp := &dns.Server{Addr: "127.0.0.53:53", Net: "udp", Handler: s}
	tcp := &dns.Server{Addr: "127.0.0.53:53", Net: "tcp", Handler: s}
	result := make(chan error, 2)
	go func() { result <- udp.ListenAndServe() }()
	go func() { result <- tcp.ListenAndServe() }()
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-result:
	}
	return errors.Join(err, udp.Shutdown(), tcp.Shutdown())
}
func validNetwork(n fc.NetworkValues) bool {
	if len(n.Peers) > 4096 || len(n.DNS) > 4 {
		return false
	}
	for name, ip := range n.Peers {
		if !strings.HasSuffix(name, ".loomplane") || net.ParseIP(ip).To4() == nil {
			return false
		}
	}
	for _, ip := range n.DNS {
		if net.ParseIP(ip).To4() == nil {
			return false
		}
	}
	return true
}
