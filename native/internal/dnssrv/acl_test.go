package dnssrv

import (
	"net"
	"testing"
)

func TestInACL(t *testing.T) {
	_, lan, _ := net.ParseCIDR("10.10.0.0/16")
	_, loop, _ := net.ParseCIDR("127.0.0.0/8")
	acl := []*net.IPNet{lan, loop}

	cases := []struct {
		name           string
		ip             net.IP
		acl            []*net.IPNet
		emptyAllows    bool
		want           bool
	}{
		{"empty ACL, empty=allow, arbitrary IP", net.ParseIP("8.8.8.8"), nil, true, true},
		{"empty ACL, empty=deny, arbitrary IP", net.ParseIP("8.8.8.8"), nil, false, false},
		{"empty ACL, empty=deny, loopback", net.ParseIP("127.0.0.1"), nil, false, false},
		{"non-empty ACL, in range", net.ParseIP("10.10.5.5"), acl, false, true},
		{"non-empty ACL, out of range", net.ParseIP("8.8.8.8"), acl, false, false},
		{"non-empty ACL, loopback in range", net.ParseIP("127.0.0.1"), acl, false, true},
		{"non-empty ACL, nil IP", nil, acl, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inACL(c.ip, c.acl, c.emptyAllows)
			if got != c.want {
				t.Errorf("inACL(%v, acl, emptyAllows=%v) = %v; want %v",
					c.ip, c.emptyAllows, got, c.want)
			}
		})
	}
}

func TestParseCIDRs(t *testing.T) {
	got := parseCIDRs([]string{"10.0.0.0/8", "not-a-cidr", "192.168.1.0/24", ""})
	if len(got) != 2 {
		t.Fatalf("expected 2 valid CIDRs, got %d", len(got))
	}
	if got[0].String() != "10.0.0.0/8" || got[1].String() != "192.168.1.0/24" {
		t.Errorf("unexpected CIDR list: %+v", got)
	}
}

func TestIpOf(t *testing.T) {
	// Simulate a net.Addr via TCP.
	addr := &net.TCPAddr{IP: net.ParseIP("10.10.0.5"), Port: 53}
	got := ipOf(addr)
	if got == nil || !got.Equal(net.ParseIP("10.10.0.5")) {
		t.Errorf("ipOf(%v) = %v; want 10.10.0.5", addr, got)
	}
}
