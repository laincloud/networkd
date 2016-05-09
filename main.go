package main

import (
	"flag"
	"fmt"
	"github.com/Sirupsen/logrus"
	logrus_syslog "github.com/Sirupsen/logrus/hooks/syslog"
	"github.com/nightlyone/lockfile"
	"log/syslog"
	"os"
	"runtime"
)

const Version = "0.1.15-dev"

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
		resolvConf      = flag.Bool("resolv.conf", false, "Enable/Disable watch /etc/resolv.conf")
		tinydns         = flag.Bool("tinydns", false, "Enable/Disable watch tinydns ip.")
		swarm           = flag.Bool("swarm", false, "Enable/Disable watch swarm ip.")
		webrouter       = flag.Bool("webrouter", false, "Enable/Disable watch webrouter ip.")
		deployd         = flag.Bool("deployd", false, "Enable/Disable watch deployd ip.")
		dnsmasq         = flag.Bool("dnsmasq", false, "Enable/Disable dnsmasq.")
		dnsmasqHost     = flag.String("dnsmasq.host", "/etc/dnsmasq.hosts", "Dnsmasq host filename")
		dnsmasqServer   = flag.String("dnsmasq.server", "/etc/dnsmasq.servers", "Dnsmasq server filename")
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
	hook, err := logrus_syslog.NewSyslogHook("", "", syslog.LOG_INFO, "")
	if err != nil {
		log.Error("Unable to connect to local syslog daemon")
	} else {
		log.Hooks.Add(hook)
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

	var server Server
	server.InitFlag(*dnsmasq, *tinydns, *swarm, *webrouter, *deployd, *resolvConf)
	server.InitIptables()
	server.InitLibNetwork(*libnetwork)
	server.InitDocker(*dockerEndpoint)
	server.InitEtcd(*etcdEndpoint)
	server.InitInterface(*netInterface)
	server.InitHostname(*hostname)
	server.InitAddress(*netAddress)
	server.InitLibkv(*etcdEndpoint)
	server.InitLainlet(*lainletEndPoint)
	server.InitWebrouter()
	server.InitDeployd()
	server.InitResolvConf()
	server.InitDomain(*domain)

	if *dnsmasq {
		server.InitDnsmasq(*dnsmasqHost, *dnsmasqServer)
	}

	if *resolvConf {
		server.RunResolvConf()
	}

	server.Run()
}
