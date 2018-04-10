package apiserver

import (
	"github.com/go-martini/martini"
	"github.com/laincloud/networkd/godns"
)

type Server struct {
	addr string
	godns *godns.Godns
	m *martini.ClassicMartini
}

func New(addr string, godns *godns.Godns) *Server {
	return &Server {
		addr:addr,
		godns:godns,
		m: martini.Classic(),
	}
}

func (srv *Server) Run() {
	m := srv.m
	m.Get("/v1/dns/config", srv.handleDnsConfig)
	m.RunOnAddr(srv.addr)
}

func (srv *Server) handleDnsConfig() string {
	return srv.godns.DumpConfig()
}
