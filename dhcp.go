package main

import (
	"fmt"
	"log"
	"net"

	"github.com/coredhcp/coredhcp"
	"github.com/coredhcp/coredhcp/config"
	_ "github.com/coredhcp/coredhcp/plugins/dns"
	_ "github.com/coredhcp/coredhcp/plugins/range"
	_ "github.com/coredhcp/coredhcp/plugins/router"
	_ "github.com/coredhcp/coredhcp/plugins/server_id"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
)

func handler4(s *coredhcp.Server, conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	log.Printf("%+v\n", req)
	s.MainHandler4(conn, peer, req)
}

func serve(s *coredhcp.Server, conn net.PacketConn) error {
	var err error

	var h = func(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
		handler4(s, conn, peer, req)
	}
	s.Server4, err = server4.NewServer("", nil, h, server4.WithConn(conn))
	if err != nil {
		return err
	}
	go func() {
		s.Server4.Serve()
	}()

	return nil
}

func DHCP(conn net.PacketConn, dns []string) error {
	fmt.Printf("[+] dhcp\n")

	conf := config.New()
	plugins := make([]*config.PluginConfig, 0)
	plugins = append(plugins,
		&config.PluginConfig{
			Name: "range",
			Args: []string{"/dev/null", "10.0.2.15", "10.0.2.100", "24h"},
		},
		&config.PluginConfig{
			Name: "router",
			Args: []string{"10.0.2.2"},
		},
		&config.PluginConfig{
			Name: "dns",
			Args: dns,
		},
		&config.PluginConfig{
			Name: "server_id",
			Args: []string{"10.0.2.2"},
		})
	conf.Server4 = &config.ServerConfig{
		Plugins: plugins,
	}

	server := coredhcp.NewServer(conf)
	_, _, err := server.LoadPlugins(server.Config)
	if err != nil {
		log.Fatal(err)
		return err
	}

	if err := serve(server, conn); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("[-] dhcp\n")
	return nil
}
