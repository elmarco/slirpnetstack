package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

var (
	fd             int
	pcapFile       string
	printCaps      bool
	netNsPath      string
	ifName         string
	remoteFwd      FwdAddrSlice
	localFwd       FwdAddrSlice
	logConnections bool
	quiet          bool
	metricAddr     AddrFlags
	gomaxprocs     int
)

func init() {
	flag.IntVar(&fd, "fd", -1, "Unix datagram socket file descriptor")
	flag.StringVar(&pcapFile, "pcap", "", "path to PCAP file")
	flag.BoolVar(&printCaps, "print-capabilities", false, "Print capabilities")
	flag.StringVar(&netNsPath, "netns", "", "path to network namespace")
	flag.StringVar(&ifName, "interface", "tun0", "interface name within netns")
	flag.Var(&remoteFwd, "R", "Connections to remote side forwarded local")
	flag.Var(&localFwd, "L", "Connections to local side forwarded remote")
	flag.BoolVar(&quiet, "quiet", false, "Print less stuff on screen")
	flag.Var(&metricAddr, "m", "Metrics addr")
	flag.IntVar(&gomaxprocs, "maxprocs", 0, "set GOMAXPROCS variable to limit cpu")
}

func main() {
	status := Main()
	os.Exit(status)
}

type State struct {
	RoutingDeny  []*net.IPNet
	RoutingAllow []*net.IPNet

	remoteUdpFwd map[string]*FwdAddr
	remoteTcpFwd map[string]*FwdAddr
}

func Main() int {
	var state State

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT)
	signal.Notify(sigCh, syscall.SIGTERM)

	// flag.Parse might be called from tests first. To avoid
	// duplicated items in list, ensure parsing is done only once.
	if flag.Parsed() == false {
		flag.Parse()
	}

	if printCaps {
		fmt.Println(`{
  "type": "slirp-helper",
  "features": [
    "ipv4",
    "ipv6"
  ]
}`)
		return 0
	}

	dnsconf := dnsReadConfig("/etc/resolv.conf")
	log.Infof("%+v", dnsconf)

	if gomaxprocs > 0 {
		runtime.GOMAXPROCS(gomaxprocs)
	}

	logConnections = !quiet

	localFwd.SetDefaultAddrs(
		netParseIP("127.0.0.1"),
		netParseIP("10.0.2.100"))
	remoteFwd.SetDefaultAddrs(
		netParseIP("10.0.2.2"),
		netParseIP("127.0.0.1"))

	state.remoteUdpFwd = make(map[string]*FwdAddr)
	state.remoteTcpFwd = make(map[string]*FwdAddr)
	// For the list of reserved IP's see
	// https://idea.popcount.org/2019-12-06-addressing/
	state.RoutingDeny = append(state.RoutingDeny,
		MustParseCIDR("0.0.0.0/8"),
		//MustParseCIDR("10.0.0.0/8"),
		MustParseCIDR("127.0.0.0/8"),
		MustParseCIDR("169.254.0.0/16"),
		MustParseCIDR("224.0.0.0/4"),
		MustParseCIDR("240.0.0.0/4"),
		MustParseCIDR("255.255.255.255/32"),
		MustParseCIDR("::/128"),
		MustParseCIDR("::1/128"),
		MustParseCIDR("::/96"),
		MustParseCIDR("::ffff:0:0:0/96"),
		MustParseCIDR("64:ff9b::/96"),
		MustParseCIDR("fc00::/7"),
		MustParseCIDR("fe80::/10"),
		MustParseCIDR("ff00::/8"),
		MustParseCIDR("fec0::/10"),
	)

	state.RoutingAllow = append(state.RoutingAllow,
		MustParseCIDR("0.0.0.0/0"),
		MustParseCIDR("::/0"),
	)

	log.SetLevel(log.Warning)
	rand.Seed(time.Now().UnixNano())

	var metrics *Metrics
	if metricAddr.Addr != nil && metricAddr.Network() != "" {
		var err error
		metrics, err = StartMetrics(metricAddr.Addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[!] Failed to start metrics: %s\n", err)
			return -2
		}
	}

	var (
		err      error    = nil
		pcapfile *os.File = nil
		tunFd    int      = fd
		tapMode  bool     = true
		tapMtu   uint32   = 1500
	)

	if pcapFile != "" {
		pcapfile, err = os.OpenFile(pcapFile, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening file: %v", err)
			return 1
		}
		defer pcapfile.Close()
	}

	if tunFd == -1 {
		tunFd, tapMode, tapMtu, err = GetTunTap(netNsPath, ifName)
		if err != nil {
			log.Infof("erro1")
			return -1
		}
	}

	// With high mtu, low packet loss and low latency over tuntap,
	// the specific value isn't that important. The only important
	// bit is that it should be at least a couple times MSS.
	bufSize := 4 * 1024 * 1024

	s := NewStack(bufSize, bufSize)

	err = AddTunTap(s, 1, tunFd, tapMode, MustParseMAC("70:71:aa:4b:29:aa"), tapMtu, pcapfile)
	if err != nil {
		return -1
	}

	StackRoutingSetup(s, 1, "10.0.2.2/24")
	StackPrimeArp(s, 1, netParseIP("10.0.2.100"))

	StackRoutingSetup(s, 1, "2001:2::2/32")

	doneChannel := make(chan bool)

	// no IP, this will catch broadcasted packets
	addr := tcpip.FullAddress{1, "", dhcpv4.ServerPort}
	p, e := gonet.DialUDP(s, &addr, nil, ipv4.ProtocolNumber)
	if e != nil {
		fmt.Fprintf(os.Stderr, "Failed to bind on DHCP: %v", e)
		return 1
	}
	conn := &KaUDPConn{Conn: p}
	DHCP(conn, dnsconf.servers)

	for _, lf := range localFwd {
		var (
			err error
			srv Listener
		)
		switch lf.network {
		case "tcp":
			srv, err = LocalForwardTCP(&state, s, &lf, doneChannel)
		case "udp":
			srv, err = LocalForwardUDP(&state, s, &lf, doneChannel)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "[!] Failed to listen on %s://%s:%d: %s\n",
				lf.network, lf.bind.Addr, lf.bind.Port, err)
		} else {
			laddr := srv.Addr()
			fmt.Printf("[+] local-fwd Local listen %s://%s\n",
				laddr.Network(), laddr.String())
		}
	}

	for i, rf := range remoteFwd {
		fmt.Printf("[+] Accepting on remote side %s://%s:%d\n",
			rf.network, rf.bind.Addr.String(), rf.bind.Port)
		switch rf.network {
		case "tcp":
			state.remoteTcpFwd[rf.BindAddr().String()] = &remoteFwd[i]
		case "udp":
			state.remoteUdpFwd[rf.BindAddr().String()] = &remoteFwd[i]
		}
	}

	tcpHandler := TcpRoutingHandler(&state)
	// Set sliding window auto-tuned value. Allow 10 concurrent
	// new connection attempts.
	fwdTcp := tcp.NewForwarder(s, 0, 10, tcpHandler)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwdTcp.HandlePacket)

	udpHandler := UdpRoutingHandler(s, &state)
	fwdUdp := udp.NewForwarder(s, udpHandler)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, fwdUdp.HandlePacket)

	// [****] Finally, the mighty event loop, waiting on signals
	pid := syscall.Getpid()
	fmt.Fprintf(os.Stderr, "[+] #%d Started\n", pid)
	syscall.Kill(syscall.Getppid(), syscall.SIGWINCH)

	for {
		select {
		case sig := <-sigCh:
			signal.Reset(sig)
			fmt.Fprintf(os.Stderr, "[-] Closing\n")
			goto stop
		}
	}
stop:
	// TODO: define semantics of graceful close on signal
	//s.Wait()
	if metrics != nil {
		metrics.Close()
	}
	return 0
}
