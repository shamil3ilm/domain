// Package dnssrv implements a DNS server that is authoritative for zones
// stored in the database and forwards everything else to configured
// upstream resolvers.
package dnssrv

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/store"
)

type Server struct {
	store        *store.Store
	cfg          *config.Config
	udpSrv       *dns.Server
	tcpSrv       *dns.Server
	allowQuery   []*net.IPNet // empty = allow all
	allowRecurse []*net.IPNet // empty = deny all
	upstream     *forwarder
}

func New(st *store.Store, cfg *config.Config) *Server {
	s := &Server{
		store:        st,
		cfg:          cfg,
		allowQuery:   parseCIDRs(cfg.AllowQueryFrom),
		allowRecurse: parseCIDRs(cfg.AllowRecursionFrom),
		upstream:     newForwarder(cfg.Upstreams),
	}
	return s
}

// Start binds and serves. Blocks until ctx is cancelled or a listener errors.
func (s *Server) Start(ctx context.Context) error {
	handler := dns.HandlerFunc(s.handle)

	s.udpSrv = &dns.Server{Addr: s.cfg.DNSAddr, Net: "udp", Handler: handler, UDPSize: 4096}
	s.tcpSrv = &dns.Server{Addr: s.cfg.DNSAddr, Net: "tcp", Handler: handler}

	errCh := make(chan error, 2)
	go func() {
		slog.Info("dns.listen", "net", "udp", "addr", s.cfg.DNSAddr)
		errCh <- s.udpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("dns.listen", "net", "tcp", "addr", s.cfg.DNSAddr)
		errCh <- s.tcpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) Shutdown() {
	if s.udpSrv != nil {
		_ = s.udpSrv.Shutdown()
	}
	if s.tcpSrv != nil {
		_ = s.tcpSrv.Shutdown()
	}
}

// handle is the main dispatch. Any panic is recovered so a single bad query
// can't take down the server.
func (s *Server) handle(w dns.ResponseWriter, r *dns.Msg) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("dns.panic", "recover", p)
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeServerFailure)
			_ = w.WriteMsg(m)
		}
	}()

	remoteIP := ipOf(w.RemoteAddr())
	if !inACL(remoteIP, s.allowQuery, true /* empty = allow */) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}

	if len(r.Question) == 0 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	qname := strings.TrimSuffix(strings.ToLower(q.Name), ".")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Longest-match: is any zone we own a suffix of qname?
	zone, err := s.store.FindZoneForName(ctx, qname)
	if err != nil {
		slog.Warn("dns.zone_lookup", "err", err)
	}
	if zone != "" {
		s.answerAuthoritatively(ctx, w, r, zone, qname, q.Qtype)
		return
	}

	// Recursion path: check the separate ACL. Public exposure is safe here
	// because non-authoritative queries from outside the recursion ACL are
	// refused instead of forwarded.
	if !inACL(remoteIP, s.allowRecurse, false /* empty = deny */) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = false
		m.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(m)
		return
	}

	s.upstream.forward(w, r)
}

// answerAuthoritatively builds and sends a response using records from the DB.
func (s *Server) answerAuthoritatively(ctx context.Context, w dns.ResponseWriter, r *dns.Msg, zone, qname string, qtype uint16) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = true

	// If the request is for SOA on the zone apex, synthesize one.
	if qtype == dns.TypeSOA && qname == zone {
		if soa := s.synthesizeSOA(ctx, zone); soa != nil {
			m.Answer = append(m.Answer, soa)
		}
		_ = w.WriteMsg(m)
		return
	}

	// Look up the exact (name, type).
	qtypeStr := dns.TypeToString[qtype]
	recs, err := s.store.LookupRecords(ctx, qname, qtypeStr)
	if err != nil {
		slog.Warn("dns.lookup", "err", err)
	}

	// CNAME chasing: if no exact match and there's a CNAME at qname, return it.
	if len(recs) == 0 && qtype != dns.TypeCNAME {
		if cns, _ := s.store.LookupRecords(ctx, qname, "CNAME"); len(cns) > 0 {
			for _, c := range cns {
				rr := buildRR(c, qname, "CNAME")
				if rr != nil {
					m.Answer = append(m.Answer, rr)
				}
				// Chase the CNAME target if it's in a zone we own.
				target := strings.TrimSuffix(strings.ToLower(c.Content), ".")
				followed, _ := s.store.LookupRecords(ctx, target, qtypeStr)
				for _, f := range followed {
					if rr := buildRR(f, target, qtypeStr); rr != nil {
						m.Answer = append(m.Answer, rr)
					}
				}
			}
			_ = w.WriteMsg(m)
			return
		}
	}

	for _, rec := range recs {
		if rr := buildRR(rec, qname, qtypeStr); rr != nil {
			m.Answer = append(m.Answer, rr)
		}
	}

	// If we're authoritative and there are no answers, it's NXDOMAIN
	// (name doesn't exist) or NODATA (name exists but no record of this type).
	// Distinguish by checking any record at this name.
	if len(m.Answer) == 0 {
		anyRecs, _ := s.store.LookupRecords(ctx, qname, "")
		if len(anyRecs) == 0 && qname != zone {
			m.Rcode = dns.RcodeNameError // NXDOMAIN
		}
		if soa := s.synthesizeSOA(ctx, zone); soa != nil {
			m.Ns = append(m.Ns, soa)
		}
	}

	_ = w.WriteMsg(m)
}

// synthesizeSOA builds a synthetic SOA record for the zone using the DB serial.
func (s *Server) synthesizeSOA(ctx context.Context, zone string) *dns.SOA {
	z, err := s.store.GetZone(ctx, zone)
	if err != nil {
		return nil
	}
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: dns.Fqdn(zone), Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600},
		Ns:      "ns1." + dns.Fqdn(zone),
		Mbox:    "hostmaster." + dns.Fqdn(zone),
		Serial:  z.Serial,
		Refresh: 3600,
		Retry:   600,
		Expire:  604800,
		Minttl:  60,
	}
}

// buildRR converts a store.Record into a dns.RR.
func buildRR(r store.Record, qname, qtype string) dns.RR {
	name := dns.Fqdn(qname)
	ttl := uint32(r.TTL)
	hdr := dns.RR_Header{Name: name, Class: dns.ClassINET, Ttl: ttl}
	switch strings.ToUpper(r.Type) {
	case "A":
		ip := net.ParseIP(r.Content).To4()
		if ip == nil {
			return nil
		}
		hdr.Rrtype = dns.TypeA
		return &dns.A{Hdr: hdr, A: ip}
	case "AAAA":
		ip := net.ParseIP(r.Content).To16()
		if ip == nil {
			return nil
		}
		hdr.Rrtype = dns.TypeAAAA
		return &dns.AAAA{Hdr: hdr, AAAA: ip}
	case "CNAME":
		hdr.Rrtype = dns.TypeCNAME
		return &dns.CNAME{Hdr: hdr, Target: dns.Fqdn(r.Content)}
	case "NS":
		hdr.Rrtype = dns.TypeNS
		return &dns.NS{Hdr: hdr, Ns: dns.Fqdn(r.Content)}
	case "PTR":
		hdr.Rrtype = dns.TypePTR
		return &dns.PTR{Hdr: hdr, Ptr: dns.Fqdn(r.Content)}
	case "TXT":
		hdr.Rrtype = dns.TypeTXT
		// Strip surrounding quotes if present.
		c := strings.TrimSpace(r.Content)
		c = strings.TrimPrefix(strings.TrimSuffix(c, `"`), `"`)
		return &dns.TXT{Hdr: hdr, Txt: []string{c}}
	case "MX":
		// content = "10 mail.example.myworld"
		parts := strings.Fields(r.Content)
		if len(parts) != 2 {
			return nil
		}
		pri, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil
		}
		hdr.Rrtype = dns.TypeMX
		return &dns.MX{Hdr: hdr, Preference: uint16(pri), Mx: dns.Fqdn(parts[1])}
	case "SRV":
		// content = "priority weight port target"
		parts := strings.Fields(r.Content)
		if len(parts) != 4 {
			return nil
		}
		pri, _ := strconv.Atoi(parts[0])
		wt, _ := strconv.Atoi(parts[1])
		port, _ := strconv.Atoi(parts[2])
		hdr.Rrtype = dns.TypeSRV
		return &dns.SRV{Hdr: hdr, Priority: uint16(pri), Weight: uint16(wt), Port: uint16(port), Target: dns.Fqdn(parts[3])}
	case "CAA":
		// content = "0 issue letsencrypt.org"
		parts := strings.SplitN(r.Content, " ", 3)
		if len(parts) != 3 {
			return nil
		}
		flag, _ := strconv.Atoi(parts[0])
		val := strings.Trim(parts[2], `"`)
		hdr.Rrtype = dns.TypeCAA
		return &dns.CAA{Hdr: hdr, Flag: uint8(flag), Tag: parts[1], Value: val}
	}
	return nil
}

// ---- ACL ------------------------------------------------------------------

// ipOf extracts the IP from a net.Addr, returning nil on failure.
func ipOf(a net.Addr) net.IP {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// inACL checks whether ip matches any CIDR in acl. When acl is empty, the
// emptyMeansAllow argument determines the outcome — for query ACL the empty
// case means "allow all" (unrestricted answering), for recursion ACL the
// empty case means "deny all" (safe default).
func inACL(ip net.IP, acl []*net.IPNet, emptyMeansAllow bool) bool {
	if len(acl) == 0 {
		return emptyMeansAllow
	}
	if ip == nil {
		return false
	}
	for _, cidr := range acl {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

// ---- Forwarder ------------------------------------------------------------

type forwarder struct {
	upstreams []string
	client    *dns.Client
	mu        sync.Mutex
	rr        int
}

func newForwarder(ups []string) *forwarder {
	return &forwarder{
		upstreams: ups,
		client:    &dns.Client{Timeout: 4 * time.Second},
	}
}

func (f *forwarder) forward(w dns.ResponseWriter, r *dns.Msg) {
	if len(f.upstreams) == 0 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}
	// Simple round-robin with fallback to the next upstream on failure.
	start := f.next()
	for i := 0; i < len(f.upstreams); i++ {
		u := f.upstreams[(start+i)%len(f.upstreams)]
		resp, _, err := f.client.Exchange(r, u)
		if err == nil && resp != nil {
			resp.Id = r.Id
			_ = w.WriteMsg(resp)
			return
		}
		slog.Debug("forward.retry", "upstream", u, "err", err)
	}
	m := new(dns.Msg)
	m.SetRcode(r, dns.RcodeServerFailure)
	_ = w.WriteMsg(m)
}

func (f *forwarder) next() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.rr
	f.rr = (f.rr + 1) % max(len(f.upstreams), 1)
	return n
}
