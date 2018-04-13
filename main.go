package main

import (
	"flag"
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/nightlyone/lockfile"
	"os"
	"runtime"
)

const Version = "2.3.3"

var log = logrus.New()

func main() {
	var (
		// TODO(xutao) support more vip interface binding
		// TODO(xutao) lainlet cannot process prefix: "http://"
		//eventHandler    = flag.String("event.hanlder", "", "Event hanlder file.")
		domain          = flag.String("domain", "", "Lain domain")
		etcdEndpoint    = flag.String("etcd.endpoint", "http://127.0.0.1:4001", "Etcd endpoint")
		lainletEndPoint = flag.String("lainlet.endpoint", "127.0.0.1:9001", "Lainlet endpoint")
		dockerEndpoint  = flag.String("docker.endpoint", "unix:///var/run/docker.sock", "Docker daemon endpoint")
		lockFilename    = flag.String("lock.filename", "/var/run/lain-networkd.pid", "Lock filename")
		netInterface    = flag.String("net.interface", "eth0", "Default interface to bind vip")
		hostname        = flag.String("hostname", "", "Hostname")
		netAddress      = flag.String("net.address", "", "Host IP address(default: net.interface's first ip)")
		libnetwork      = flag.Bool("libnetwork", false, "Enable/Disable libnetwork.")
		tinydns         = flag.Bool("tinydns", false, "Enable/Disable watch tinydns ip.")
		swarm           = flag.Bool("swarm", false, "Enable/Disable watch swarm ip.")
		webrouter       = flag.Bool("webrouter", false, "Enable/Disable watch webrouter ip.")
		streamrouter    = flag.Bool("streamrouter", false, "Enable/Disable watch streamrouter vips and ports.")
		deployd         = flag.Bool("deployd", false, "Enable/Disable watch deployd ip.")
		acl             = flag.Bool("acl", false, "Enable/Disable acl.")
		dnsAddr         = flag.String("godns.addr", "127.0.0.1:53", "godnds' listen address")
		apiAddr          = flag.String("api.addr", "127.0.0.1:3000", "api server's listen address")
		printVersion    = flag.Bool("version", false, "Print the version and exit.")
		verbose         = flag.Bool("verbose", false, "Print more info.")
	)
	flag.Parse()

	if *printVersion == true {
		fmt.Println("networkd Version: " + Version)
		fmt.Println("Go Version: " + runtime.Version())
		fmt.Println("Go OS/Arch: " + runtime.GOOS + "/" + runtime.GOARCH)
		os.Exit(0)
	}

	if *verbose == true {
		log.Level = logrus.DebugLevel
	} else {
		log.Level = logrus.InfoLevel
	}

	lock, err := lockfile.New(*lockFilename)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Fatal("Cannot init lock")
	}
	err = lock.TryLock()

	// Error handling is essential, as we only try to get the lock.
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Fatal("Cannot lock file")
	}

	defer lock.Unlock()

	var agt Agent
	agt.InitFlag(*tinydns, *swarm, *webrouter, *deployd, *acl, *streamrouter)
	agt.InitIptables()
	agt.InitLibNetwork(*libnetwork)
	agt.InitDocker(*dockerEndpoint)
	agt.InitEtcd(*etcdEndpoint)
	agt.InitCalico(*etcdEndpoint)
	agt.InitInterface(*netInterface)
	agt.InitHostname(*hostname)
	agt.InitAddress(*netAddress)
	agt.InitLibkv(*etcdEndpoint)
	agt.InitLainlet(*lainletEndPoint)
	agt.InitWebrouter()
	agt.InitStreamrouter()
	agt.InitDeployd()
	agt.InitDomain(*domain)

	agt.InitGodns(*dnsAddr)
	agt.InitApiServer(*apiAddr)

	if *acl {
		agt.InitAcl()
	}

	agt.Run()
}
