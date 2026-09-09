// dig — tiny DNS query tool for testing the privatedns server. Not part of
// the production binary; just a convenient stand-in for nslookup/dig on
// hosts that lack them or can't specify a port.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/miekg/dns"
)

func main() {
	server := flag.String("s", "127.0.0.1:15353", "DNS server address")
	qtype := flag.String("t", "A", "record type")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: dig -s host:port -t A name")
		os.Exit(2)
	}
	name := dns.Fqdn(flag.Arg(0))
	tp, ok := dns.StringToType[strings.ToUpper(*qtype)]
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown type:", *qtype)
		os.Exit(2)
	}

	m := new(dns.Msg)
	m.SetQuestion(name, tp)
	m.RecursionDesired = true

	c := new(dns.Client)
	resp, rtt, err := c.Exchange(m, *server)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Printf(";; Server: %s\n;; Time:   %s\n;; Rcode:  %s\n;; Answer count: %d\n\n",
		*server, rtt, dns.RcodeToString[resp.Rcode], len(resp.Answer))

	for _, a := range resp.Answer {
		fmt.Println(a)
	}
	if len(resp.Answer) == 0 {
		for _, a := range resp.Ns {
			fmt.Println(";; authority:", a)
		}
	}
}
