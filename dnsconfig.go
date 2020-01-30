package main

import (
	"bufio"
	"net"
	"os"
	"strings"
)

type dnsConfig struct {
	servers    []string // server addresses (in host:port form) to use
	err        error    // any error that occurs during open of resolv.conf
	unknownOpt bool     // anything unknown was encountered
}

var (
	defaultNS = []string{"127.0.0.1", "[::1]"}
)

// See resolv.conf(5) on a Linux machine.
func dnsReadConfig(filename string) *dnsConfig {
	conf := &dnsConfig{}

	file, err := os.Open(filename)
	if err != nil {
		conf.servers = defaultNS
		conf.err = err
		return conf
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 && (line[0] == ';' || line[0] == '#') {
			// comment.
			continue
		}
		f := strings.Fields(line)
		if len(f) < 1 {
			continue
		}
		switch f[0] {
		case "nameserver": // add one name server
			if len(f) > 1 && len(conf.servers) < 3 { // small, but the standard limit
				// One more check: make sure server name is
				// just an IP address. Otherwise we need DNS
				// to look it up.
				if net.ParseIP(f[1]) != nil {
					conf.servers = append(conf.servers, f[1])
				}
			}
		default:
			conf.unknownOpt = true
		}
	}
	if len(conf.servers) == 0 {
		conf.servers = defaultNS
	}
	return conf
}
