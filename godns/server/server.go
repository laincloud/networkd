package server

import (
	"net"
	"time"

	"github.com/miekg/dns"
	"github.com/Sirupsen/logrus"
	"sync"
)

const (
	HOSTS_PATH = "/etc/hosts"
	HOSTS_TTL  = 600

	RESOLV_CONF     = "/etc/resolv.conf"
	RESOLV_TIMEOUT  = 5
	RESOLV_INTERVAL = 200
	RESOLV_EDNS0_ON = true

	CACHE_EXPIRE   = 600     // 10 minutes
	CACHE_MAXCOUNT = 20000   // If set zero. The Sum of cache itmes will be unlimit.

	notIPQuery = 0
	_IP4Query  = 4
	_IP6Query  = 6
)

var (
	glog *logrus.Logger
)

type Question struct {
	qname  string
	qtype  string
	qclass string
}

func (q *Question) String() string {
	return q.qname + " " + q.qclass + " " + q.qtype
}

type Server struct {
	wg       sync.WaitGroup
	addr     string
	rTimeout time.Duration
	wTimeout time.Duration

	isRunning bool
	stopCh    chan bool

	resolver        *Resolver
	cache, negCache Cache

	ldata *LocalData

	udpSrv *dns.Server
	tcpSrv *dns.Server

	// resolv.conf
	resolvConfFlag      bool
	resolvConfStopCh    chan struct{}
	resolvConfIsRunning bool
}

func New(addr string, log *logrus.Logger) *Server {
	var (
		resolver        *Resolver
		cache, negCache Cache
	)
	glog = log

	resolver = NewResolver(addr)

	cache = &MemoryCache{
		Backend:  make(map[string]Mesg, CACHE_MAXCOUNT),
		Expire:   time.Duration(CACHE_EXPIRE) * time.Second,
		Maxcount: CACHE_MAXCOUNT,
	}
	negCache = &MemoryCache{
		Backend:  make(map[string]Mesg),
		Expire:   time.Duration(CACHE_EXPIRE) * time.Second / 2,
		Maxcount: CACHE_MAXCOUNT,
	}

	server := &Server{
		addr:     addr,
		rTimeout: 5 * time.Second,
		wTimeout: 5 * time.Second,
		stopCh:   make(chan bool),
		resolver: resolver,
		ldata:    NewLocalData(),
		cache:    cache,
		negCache: negCache,
		resolvConfFlag: true,
		resolvConfStopCh: make(chan struct{}),
		resolvConfIsRunning: false,
	}
	server.InitResolvConf()
	return server
}

func (srv *Server) Run() {
	tcpHandler := dns.NewServeMux()
	tcpHandler.HandleFunc(".", srv.DoTCP)

	udpHandler := dns.NewServeMux()
	udpHandler.HandleFunc(".", srv.DoUDP)

	tcpServer := &dns.Server{
		Addr:         srv.addr,
		Net:          "tcp",
		Handler:      tcpHandler,
		ReadTimeout:  srv.rTimeout,
		WriteTimeout: srv.wTimeout,
	}

	udpServer := &dns.Server{
		Addr:         srv.addr,
		Net:          "udp",
		Handler:      udpHandler,
		UDPSize:      65535,
		ReadTimeout:  srv.rTimeout,
		WriteTimeout: srv.wTimeout,
	}
	srv.tcpSrv = tcpServer
	srv.udpSrv = udpServer

	srv.startDnsServers()
	srv.RunResolvConf()

	srv.isRunning = true
	<-srv.stopCh
	srv.shutdown()
	srv.wg.Wait()
	srv.isRunning = false
	glog.Infof("Dns server stopped.")
}

func (srv *Server) Stop() {
	if srv.isRunning {
		srv.StopResolvConf()
		close(srv.stopCh)
	}
}

func (self *Server) ReplaceHosts(data map[string][]string) {
	glog.Infof("replace hosts %v", data)
	self.ldata.ReplaceHosts(data)
}

func (self *Server) ReplaceAddresses(data map[string] []string) {
	glog.Infof("replace Address %v", data)
	self.ldata.ReplaceWildcardHosts(data)
}

func (self *Server) ReplaceDomainServers(data map[string][]string) {
	glog.Infof("replace domain servers %v", data)
	self.resolver.ReplaceDomainServers(data)
}

func (self *Server) DumpAllConfig() string {
	data := self.ldata.DumpConfig()
	data = append(data, self.resolver.DumpConfig()...)
	return string(data)
}

func (srv *Server) startDnsServers() {
	glog.Infof("Start tcp dns server on %s", srv.addr)
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()

		err := srv.tcpSrv.ListenAndServe()
		if err != nil {
			glog.Errorf("Failed to start tcp dns server, error: %s", srv.addr, err.Error())
		}
	}()

	glog.Infof("Start udp dns server on %s", srv.addr)
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		err := srv.udpSrv.ListenAndServe()
		if err != nil {
			glog.Errorf("Failed to start udp dns server, error: %s", srv.addr, err.Error())
		}
	}()
}

func (srv *Server) shutdown() {
	if srv.isRunning {
		srv.tcpSrv.Shutdown()
		srv.udpSrv.Shutdown()
	}
}

func (srv *Server) do(Net string, w dns.ResponseWriter, req *dns.Msg) {
	q := req.Question[0]
	Q := Question{UnFqdn(q.Name), dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]}

	var remote net.IP
	if Net == "tcp" {
		remote = w.RemoteAddr().(*net.TCPAddr).IP
	} else {
		remote = w.RemoteAddr().(*net.UDPAddr).IP
	}
	glog.Infof("%s lookup　%s", remote, Q.String())

	IPQuery := isIPQuery(q)

	// Query hosts
	if IPQuery > 0 {
		if ips, ok := srv.ldata.GetIpList(Q.qname, IPQuery); ok {
			m := new(dns.Msg)
			m.SetReply(req)

			switch IPQuery {
			case _IP4Query:
				rr_header := dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    HOSTS_TTL,
				}
				for _, ip := range ips {
					a := &dns.A{rr_header, ip}
					m.Answer = append(m.Answer, a)
				}
			case _IP6Query:
				rr_header := dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeAAAA,
					Class:  dns.ClassINET,
					Ttl:    HOSTS_TTL,
				}
				for _, ip := range ips {
					aaaa := &dns.AAAA{rr_header, ip}
					m.Answer = append(m.Answer, aaaa)
				}
			}

			w.WriteMsg(m)
			glog.Debugf("%s found in hosts file", Q.qname)
			return
		} else {
			glog.Debugf("%s didn't found in hosts file", Q.qname)
		}
	}

	// Only query cache when qtype == 'A'|'AAAA' , qclass == 'IN'
	key := KeyGen(Q)
	if IPQuery > 0 {
		mesg, err := srv.cache.Get(key)
		if err != nil {
			if mesg, err = srv.negCache.Get(key); err != nil {
				glog.Debugf("%s didn't hit cache", Q.String())
			} else {
				glog.Debugf("%s hit negative cache", Q.String())
				dns.HandleFailed(w, req)
				return
			}
		} else {
			glog.Debugf("%s hit cache", Q.String())
			// we need this copy against concurrent modification of Id
			msg := *mesg
			msg.Id = req.Id
			w.WriteMsg(&msg)
			return
		}
	}

	mesg, err := srv.resolver.Lookup(Net, req)

	if err != nil {
		glog.Warnf("Resolve query error %s", err)
		dns.HandleFailed(w, req)

		// cache the failure, too!
		if err = srv.negCache.Set(key, nil); err != nil {
			glog.Warnf("Set %s negative cache failed: %v", Q.String(), err)
		}
		return
	}

	w.WriteMsg(mesg)

	if IPQuery > 0 && len(mesg.Answer) > 0 {
		err = srv.cache.Set(key, mesg)
		if err != nil {
			glog.Warnf("Set %s cache failed: %s", Q.String(), err.Error())
		}
		glog.Debugf("Insert %s into cache", Q.String())
	}
}

func (srv *Server) DoTCP(w dns.ResponseWriter, req *dns.Msg) {
	srv.do("tcp", w, req)
}

func (srv *Server) DoUDP(w dns.ResponseWriter, req *dns.Msg) {
	srv.do("udp", w, req)
}

func isIPQuery(q dns.Question) int {
	if q.Qclass != dns.ClassINET {
		return notIPQuery
	}

	switch q.Qtype {
	case dns.TypeA:
		return _IP4Query
	case dns.TypeAAAA:
		return _IP6Query
	default:
		return notIPQuery
	}
}

func UnFqdn(s string) string {
	if dns.IsFqdn(s) {
		return s[:len(s)-1]
	}
	return s
}
