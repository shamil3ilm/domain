package dnssrv

import (
	"context"
	"log/slog"
	"time"

	"github.com/miekg/dns"

	"github.com/privatedns/native/internal/metrics"
)

// handleAXFR streams a full zone to an authorized secondary. Called from
// handle() when the request is AXFR (RFC 5936). The reply format is:
//
//	SOA
//	<every RR in the zone>
//	SOA
//
// Miekg's server wants us to write messages back over the same TCP
// connection via w.WriteMsg — the transport is TCP-only for AXFR. UDP AXFR
// is ancient history and not supported here.
//
// Auth: IP allow-list only (TSIG is not implemented in this version). If
// the source isn't in AXFRAllowFrom we return REFUSED and label the metric
// so operators can see abuse.
func (s *Server) handleAXFR(w dns.ResponseWriter, r *dns.Msg) {
	remoteIP := ipOf(w.RemoteAddr())

	// The AXFR ACL is intentionally deny-by-default. We don't fold it into
	// AllowQueryFrom because normal query permission and transfer permission
	// have very different blast radii: query-refused just fails one lookup;
	// transfer-permitted exposes the whole zone contents.
	if !inACL(remoteIP, s.allowAXFR, false /* empty = deny */) {
		metrics.DNSACLRefusals.WithLabelValues("axfr").Inc()
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	zoneName := trimTrailingDot(q.Name)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// The requested name must match a zone we own — no arbitrary transfer.
	zone, err := s.store.GetZone(ctx, zoneName)
	if err != nil {
		slog.Info("axfr.no_such_zone", "zone", zoneName, "err", err)
		m := new(dns.Msg)
		// RFC 5936 §2.2.1: NOTAUTH signals "we don't have that zone".
		m.SetRcode(r, dns.RcodeNotAuth)
		_ = w.WriteMsg(m)
		return
	}

	recs, err := s.store.ListRecordsInZone(ctx, zoneName)
	if err != nil {
		slog.Warn("axfr.list_records", "zone", zoneName, "err", err)
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}

	// Build the RR list: leading SOA, every record, trailing SOA.
	soa := &dns.SOA{
		Hdr: dns.RR_Header{
			Name: dns.Fqdn(zoneName), Rrtype: dns.TypeSOA,
			Class: dns.ClassINET, Ttl: 3600,
		},
		Ns:      "ns1." + dns.Fqdn(zoneName),
		Mbox:    "hostmaster." + dns.Fqdn(zoneName),
		Serial:  zone.Serial,
		Refresh: 3600,
		Retry:   600,
		Expire:  604800,
		Minttl:  60,
	}

	rrs := make([]dns.RR, 0, len(recs)+2)
	rrs = append(rrs, soa)
	for _, rec := range recs {
		if rec.Disabled {
			continue
		}
		// The synthesized SOA at the apex is already at rrs[0]; skip any
		// stored SOA row to avoid a duplicate.
		if rec.Type == "SOA" {
			continue
		}
		rr := buildRR(rec, rec.Name, rec.Type)
		if rr != nil {
			rrs = append(rrs, rr)
		}
	}
	rrs = append(rrs, soa)

	// miekg's Transfer helper handles the TCP framing (chunking large zones
	// into multiple envelope messages, one goroutine feeding a channel).
	tr := new(dns.Transfer)
	ch := make(chan *dns.Envelope, 1)
	go func() {
		ch <- &dns.Envelope{RR: rrs}
		close(ch)
	}()
	if err := tr.Out(w, r, ch); err != nil {
		slog.Warn("axfr.transfer_out", "zone", zoneName, "err", err)
		// Best effort: nothing we can send now, the client has already read
		// whatever we managed to write.
		return
	}

	slog.Info("axfr.served",
		"zone", zoneName, "records", len(rrs)-2, "peer", remoteIP)
}

// trimTrailingDot returns name without a trailing "." — the zone lookup key
// on our side stores names without the FQDN terminator.
func trimTrailingDot(name string) string {
	if n := len(name); n > 0 && name[n-1] == '.' {
		return name[:n-1]
	}
	return name
}
